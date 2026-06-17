// Package wiring implements deploy.Wirer: after a topology's VMs are deployed
// and booted, it logs into the VSA's Veeam REST API and registers the other
// appliances — VIA proxy, VIA hardened repository — and, for HA topologies,
// creates the 2-node cluster. It is the orchestration layer on top of the
// internal/veeam REST client.
package wiring

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/BaptisteTellier/autodeploy-web/internal/deploy"
	"github.com/BaptisteTellier/autodeploy-web/internal/veeam"
)

// DefaultPairingCode is the appliance handshake code accepted for ~1h after a
// VIA boots (the autodeploy default).
const DefaultPairingCode = "000000"

// Config controls how the wiring connects to the VSA and shapes the topology.
type Config struct {
	Username        string        // VSA REST user (default "veeamadmin")
	Password        string        // VSA REST password
	Insecure        bool          // skip TLS verification (self-signed VSA cert)
	ClusterDNSName  string        // HA cluster DNS name (HA topologies only)
	ClusterEndpoint string        // HA cluster floating VIP IP (required by VBR in same-subnet mode)
	RepoPath        string        // hardened-repo path (default /var/lib/veeam/backups)
	ImmutableDays   int           // hardened-repo immutability days (default 7)
	SessionTimeout  time.Duration // how long to wait per async infra session
	LicensePath     string        // optional .lic file to install on the VSA via REST ("" = skip)

	// Advanced post-wiring options, applied on the primary VSA after the
	// topology is registered. Zero values are skipped.
	NodeExporter     bool   // enable the Prometheus node_exporter metrics endpoint
	NodeExporterTLS  bool   // serve node_exporter over TLS
	NodeExporterUser string // optional basic-auth username ("" = no auth)
	NodeExporterPass string // optional basic-auth password
	SyslogServer     string // syslog target host/IP ("" = skip)
	SyslogPort       int    // syslog port (default 514)
	SyslogProtocol   string // "Udp" | "Tcp" | "Tls" (default Udp)
	S3               *S3Config
}

// S3Config describes an optional object-storage backup repository to add during
// wiring (a cloud credential is created first, then the repository).
type S3Config struct {
	Name          string // repository name
	Compatible    bool   // true => S3-compatible (ServicePoint required); false => Amazon S3
	ServicePoint  string // S3-compatible endpoint URL
	Region        string // AWS region id (Amazon) or provider region (compatible)
	Bucket        string
	Folder        string
	AccessKey     string
	SecretKey     string
	ImmutableDays int // 0 = immutability disabled

	// MountServerNode is the Name (hostname) of a previously-registered VIA-Proxy
	// node to pin as the Linux mount server for this repository. When non-empty,
	// applyAdvanced resolves its managed-server id and passes it to AddS3Repository.
	// "" means let VBR choose automatically (existing behaviour).
	MountServerNode string

	OverwriteOwner bool // take over the bucket if already owned by another backup server
}

// Wirer registers a deployed topology into its VSA. It satisfies deploy.Wirer.
type Wirer struct {
	cfg Config
}

// New builds a Wirer with sane defaults.
func New(cfg Config) *Wirer {
	if cfg.Username == "" {
		cfg.Username = "veeamadmin"
	}
	if cfg.RepoPath == "" {
		cfg.RepoPath = "/var/lib/veeam/backups"
	}
	if cfg.ImmutableDays <= 0 {
		cfg.ImmutableDays = 7
	}
	if cfg.SessionTimeout <= 0 {
		cfg.SessionTimeout = 15 * time.Minute
	}
	return &Wirer{cfg: cfg}
}

// isVSA / isHR / isProxy classify a node by its role label.
func isVSA(role string) bool   { return strings.HasPrefix(role, "VSA") }
func isHR(role string) bool    { return strings.Contains(role, "HR") }
func isProxy(role string) bool { return strings.Contains(role, "Proxy") }

func pairing(n deploy.NodeDeploy) string {
	if n.PairingCode != "" {
		return n.PairingCode
	}
	return DefaultPairingCode
}

// Wire implements deploy.Wirer.
func (w *Wirer) Wire(ctx context.Context, nodes []deploy.NodeDeploy, log func(string)) error {
	// Split nodes by role.
	var vsas, vias []deploy.NodeDeploy
	for _, n := range nodes {
		if isVSA(n.Role) {
			vsas = append(vsas, n)
		} else {
			vias = append(vias, n)
		}
	}
	if len(vsas) == 0 {
		return fmt.Errorf("no VSA node in topology — nothing to wire into")
	}
	primary := vsas[0]
	if primary.IP == "" {
		return fmt.Errorf("VSA node %q has no IP", primary.Name)
	}

	endpoint := fmt.Sprintf("https://%s:9419", primary.IP)
	client := veeam.New(veeam.Config{
		BaseURL:  endpoint,
		Username: w.cfg.Username,
		Password: w.cfg.Password,
		Insecure: w.cfg.Insecure,
	})

	log(fmt.Sprintf("waiting for VSA REST at %s …", endpoint))
	if err := w.waitReady(ctx, client, log); err != nil {
		return err
	}
	log("VSA REST is up — authenticated.")
	defer client.Logout(context.Background())

	// Install the license first: a remote-kickstarted VSA cannot carry the .lic
	// inside the ISO, so it boots unlicensed. Pushing it over REST (NoLicense
	// role) before registering infrastructure makes the cluster fully licensed.
	if w.cfg.LicensePath != "" {
		if err := w.installLicense(ctx, client, log); err != nil {
			return fmt.Errorf("install license: %w", err)
		}
	}

	// Register each VIA node (proxy / hardened repo) once it answers on the
	// network (its unattended install must be finished before pairing works).
	// Operations are idempotent (find-before-add), mirroring the reference, so
	// the wiring can be safely re-run after a partial failure.
	var hardenedRepoID string
	for _, n := range vias {
		if n.IP == "" {
			return fmt.Errorf("node %q (%s) has no IP", n.Name, n.Role)
		}
		log(fmt.Sprintf("waiting for %s (%s) to come up…", n.IP, n.Role))
		if err := waitNodeUp(ctx, n.IP, log); err != nil {
			return fmt.Errorf("node %s not reachable: %w", n.IP, err)
		}

		hostID, err := client.FindManagedServerByName(ctx, n.IP)
		if err != nil {
			return fmt.Errorf("lookup managed server %s: %w", n.IP, err)
		}
		if hostID == "" {
			log(fmt.Sprintf("adding Linux host %s (%s)…", n.IP, n.Role))
			sess, err := client.AddLinuxHost(ctx, n.IP, n.Role, pairing(n), "")
			if err != nil {
				return fmt.Errorf("add host %s: %w", n.IP, err)
			}
			if err := client.WaitSession(ctx, sess, 10*time.Second, w.cfg.SessionTimeout); err != nil {
				return fmt.Errorf("add host %s: %w", n.IP, err)
			}
			if hostID, err = client.FindManagedServerByName(ctx, n.IP); err != nil || hostID == "" {
				return fmt.Errorf("resolve managed server %s: %v", n.IP, err)
			}
		} else {
			log(fmt.Sprintf("Linux host %s already registered — skipping.", n.IP))
		}

		switch {
		case isHR(n.Role):
			repoName := "HR-" + n.Name
			id, err := client.FindRepositoryByName(ctx, repoName)
			if err != nil {
				return fmt.Errorf("lookup repo %s: %w", repoName, err)
			}
			if id == "" {
				log(fmt.Sprintf("creating hardened repository on %s…", n.IP))
				rs, err := client.AddHardenedRepository(ctx, repoName, hostID, w.cfg.RepoPath, "", true, w.cfg.ImmutableDays)
				if err != nil {
					return fmt.Errorf("add hardened repo %s: %w", n.IP, err)
				}
				if err := client.WaitSession(ctx, rs, 10*time.Second, w.cfg.SessionTimeout); err != nil {
					return fmt.Errorf("hardened repo %s: %w", n.IP, err)
				}
				if id, err = client.FindRepositoryByName(ctx, repoName); err != nil {
					return fmt.Errorf("resolve repo %s: %w", repoName, err)
				}
			} else {
				log(fmt.Sprintf("hardened repository %q already exists — skipping.", repoName))
			}
			// First hardened repo in the topology becomes the HA config-backup
			// target (keep the first, ignore any later ones).
			if hardenedRepoID == "" {
				hardenedRepoID = id
			}
		case isProxy(n.Role):
			log(fmt.Sprintf("registering VMware proxy on %s…", n.IP))
			ps, err := client.AddVmwareProxy(ctx, hostID, 4)
			if err != nil {
				return fmt.Errorf("add proxy %s: %w", n.IP, err)
			}
			if err := client.WaitSession(ctx, ps, 10*time.Second, w.cfg.SessionTimeout); err != nil {
				return fmt.Errorf("proxy %s: %w", n.IP, err)
			}
		}
	}

	// HA topology: two VSA nodes → move config backup off the default repo onto
	// the hardened repo, remove the default repo, then create the cluster. This
	// prerequisite ordering follows the vbr-ha-cluster reference (Steps 3.5/4/7).
	if len(vsas) >= 2 {
		if hardenedRepoID != "" {
			log("redirecting config backup to the first hardened repository…")
			if err := client.RedirectConfigBackup(ctx, hardenedRepoID); err != nil {
				return fmt.Errorf("redirect config backup: %w", err)
			}
			log("removing the Default Backup Repository…")
			if err := w.removeDefaultRepository(ctx, client, log); err != nil {
				return fmt.Errorf("remove default repository: %w", err)
			}
		}
		if err := w.createHA(ctx, client, vsas[0], vsas[1], log); err != nil {
			return err
		}
	}

	// Advanced options (node_exporter / syslog / S3 repository) on the primary VSA.
	if err := w.applyAdvanced(ctx, client, nodes, log); err != nil {
		return err
	}
	return nil
}

// applyAdvanced applies the optional post-wiring settings on the primary VSA:
// the Prometheus node_exporter endpoint, a syslog forwarding target, and an
// object-storage (S3 / S3-compatible) backup repository. Each block is skipped
// when its config is zero-valued.
func (w *Wirer) applyAdvanced(ctx context.Context, client *veeam.Client, nodes []deploy.NodeDeploy, log func(string)) error {
	if w.cfg.NodeExporter {
		log("enabling node_exporter metrics endpoint…")
		if err := client.SetNodeExporter(ctx, true, w.cfg.NodeExporterTLS, w.cfg.NodeExporterUser, w.cfg.NodeExporterPass); err != nil {
			return fmt.Errorf("node_exporter: %w", err)
		}
	}
	if w.cfg.SyslogServer != "" {
		log(fmt.Sprintf("configuring syslog forwarding to %s…", w.cfg.SyslogServer))
		if err := client.SetSyslog(ctx, w.cfg.SyslogServer, w.cfg.SyslogPort, w.cfg.SyslogProtocol); err != nil {
			return fmt.Errorf("syslog: %w", err)
		}
	}
	if s := w.cfg.S3; s != nil {
		log(fmt.Sprintf("creating object-storage repository %q…", s.Name))
		credID, err := client.CreateCloudCredentials(ctx, s.AccessKey, s.SecretKey, "autodeploy S3 "+s.Name)
		if err != nil {
			return fmt.Errorf("S3 credentials: %w", err)
		}

		// Optionally pin a Linux mount server by resolving the nominated node's
		// managed-server id. Failures are non-fatal: log a warning and proceed
		// without a mount server (VBR will pick one automatically).
		var mountServerID string
		if s.MountServerNode != "" {
			var mountNode *deploy.NodeDeploy
			for i := range nodes {
				if nodes[i].Name == s.MountServerNode {
					mountNode = &nodes[i]
					break
				}
			}
			if mountNode == nil {
				log(fmt.Sprintf("S3 mount server: node %q not found in topology — skipping mount server pin", s.MountServerNode))
			} else if mountNode.IP == "" {
				log(fmt.Sprintf("S3 mount server: node %q has no IP — skipping mount server pin", s.MountServerNode))
			} else {
				id, err := client.FindManagedServerByName(ctx, mountNode.IP)
				if err != nil {
					log(fmt.Sprintf("S3 mount server: lookup %s failed (%v) — skipping mount server pin", mountNode.IP, err))
				} else if id == "" {
					log(fmt.Sprintf("S3 mount server: %s (%s) not found in managed servers — skipping mount server pin", s.MountServerNode, mountNode.IP))
				} else {
					log(fmt.Sprintf("S3: using %s (%s) as Linux mount server", s.MountServerNode, mountNode.IP))
					mountServerID = id
				}
			}
		}

		// VBR's repository-add only OPENS an existing folder, so create it first
		// (what the GUI's "New Folder" button does). newFolder is idempotent — it
		// returns 201 whether the folder is new or already exists — so no
		// browse/exists pre-check is needed. Any error is non-fatal: AddS3Repository
		// will fail clearly afterwards if the folder truly can't be opened.
		if s.Compatible && s.Folder != "" {
			if err := client.NewS3CompatibleFolder(ctx, credID, s.ServicePoint, s.Region, s.Bucket, s.Folder); err != nil {
				log(fmt.Sprintf("S3: ensure folder %q: %v (continuing)", s.Folder, err))
			} else {
				log(fmt.Sprintf("S3: folder %q ready in bucket %s", s.Folder, s.Bucket))
			}
		}

		sess, err := client.AddS3Repository(ctx, veeam.S3RepoSpec{
			Name:           s.Name,
			Description:    "autodeploy object storage",
			CredentialsID:  credID,
			Compatible:     s.Compatible,
			ServicePoint:   s.ServicePoint,
			RegionID:       s.Region,
			Bucket:         s.Bucket,
			Folder:         s.Folder,
			ImmutableDays:  s.ImmutableDays,
			MountServerID:  mountServerID,
			OverwriteOwner: s.OverwriteOwner,
		})
		if err != nil {
			return fmt.Errorf("S3 repository: %w", err)
		}
		if err := client.WaitSession(ctx, sess, 10*time.Second, w.cfg.SessionTimeout); err != nil {
			return fmt.Errorf("S3 repository: %w", err)
		}
	}
	return nil
}

// installLicense reads the .lic file and pushes it to the VSA via REST. It is
// idempotent enough to re-run: if a valid license is already present it skips
// the install (re-wiring after a partial failure should not error).
func (w *Wirer) installLicense(ctx context.Context, client *veeam.Client, log func(string)) error {
	if cur, err := client.GetLicense(ctx); err == nil && strings.EqualFold(cur.Status, "Valid") {
		log(fmt.Sprintf("license already installed (%s, %s) — skipping.", cur.Edition, cur.LicensedTo))
		return nil
	}
	data, err := os.ReadFile(w.cfg.LicensePath)
	if err != nil {
		return fmt.Errorf("read license file %s: %w", w.cfg.LicensePath, err)
	}
	log(fmt.Sprintf("installing license (%s)…", w.cfg.LicensePath))
	lic, err := client.InstallLicense(ctx, data)
	if err != nil {
		return err
	}
	log(fmt.Sprintf("license installed: status=%s edition=%s licensedTo=%q", lic.Status, lic.Edition, lic.LicensedTo))
	return nil
}

// removeDefaultRepository deletes every backup in "Default Backup Repository"
// then removes the repo (reference Step 4). No-op if the repo is absent.
func (w *Wirer) removeDefaultRepository(ctx context.Context, client *veeam.Client, log func(string)) error {
	const name = "Default Backup Repository"
	id, err := client.FindRepositoryByName(ctx, name)
	if err != nil {
		return err
	}
	if id == "" {
		log("Default Backup Repository not found — skipping.")
		return nil
	}
	backups, err := client.ListBackups(ctx, id)
	if err != nil {
		return err
	}
	for _, b := range backups {
		sess, err := client.DeleteBackup(ctx, b)
		if err != nil {
			return fmt.Errorf("delete backup %s: %w", b, err)
		}
		if sess != "" {
			if err := client.WaitSession(ctx, sess, 10*time.Second, w.cfg.SessionTimeout); err != nil {
				return fmt.Errorf("delete backup %s: %w", b, err)
			}
		}
	}
	return client.DeleteRepository(ctx, id)
}

// waitNodeUp waits until the host answers on one of the Veeam appliance ports
// (6160 = deployer service used for pairing, 443, 22). The overall deadline is
// the caller's context (the deploy WireTimeout).
func waitNodeUp(ctx context.Context, ip string, log func(string)) error {
	ports := []string{"6160", "443", "22"}
	dialer := net.Dialer{Timeout: 3 * time.Second}
	for {
		for _, port := range ports {
			conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(ip, port))
			if err == nil {
				_ = conn.Close()
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(15 * time.Second):
			log(fmt.Sprintf("…%s still installing/booting", ip))
		}
	}
}

// waitReady polls the VSA OAuth endpoint until authentication succeeds.
// It relies entirely on ctx for its deadline (set by the caller's WireTimeout).
func (w *Wirer) waitReady(ctx context.Context, client *veeam.Client, log func(string)) error {
	for {
		if err := client.Authenticate(ctx); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("VSA REST not reachable: %w", ctx.Err())
		case <-time.After(20 * time.Second):
			log("…still waiting for the VSA to finish installing / boot")
		}
	}
}

// createHA builds the secondary-node credentials + certificate and creates the
// HA cluster.
func (w *Wirer) createHA(ctx context.Context, client *veeam.Client, primary, secondary deploy.NodeDeploy, log func(string)) error {
	if secondary.IP == "" {
		return fmt.Errorf("secondary VSA %q has no IP", secondary.Name)
	}
	log(fmt.Sprintf("waiting for secondary VSA (%s) to come up…", secondary.IP))
	if err := waitNodeUp(ctx, secondary.IP, log); err != nil {
		return fmt.Errorf("secondary VSA not reachable: %w", err)
	}
	log(fmt.Sprintf("creating HA cluster (%s + %s)…", primary.IP, secondary.IP))

	credID, err := client.CreateCredentials(ctx, w.cfg.Username, w.cfg.Password, "HA secondary node")
	if err != nil {
		return fmt.Errorf("HA credentials: %w", err)
	}
	certB64, _, err := client.ConnectionCertificate(ctx, secondary.IP, credID)
	if err != nil {
		return fmt.Errorf("HA secondary certificate: %w", err)
	}
	dns := w.cfg.ClusterDNSName
	if dns == "" {
		dns = primary.Name // fall back to the primary hostname
	}
	// VBR requires the cluster floating IP (clusterEndpoint) in same-subnet
	// (non-cross-subnet) mode — fail early with a clear message if it's missing.
	if w.cfg.ClusterEndpoint == "" {
		return fmt.Errorf("HA cluster endpoint (floating VIP IP) is required — set the 'Cluster IP' field")
	}
	sess, err := client.CreateHACluster(ctx, veeam.HASpec{
		PrimaryNodeIP:          primary.IP,
		SecondaryNodeIP:        secondary.IP,
		SecondaryCredentialsID: credID,
		ClusterDNSName:         dns,
		ClusterEndpoint:        w.cfg.ClusterEndpoint,
		CertificatePEMBase64:   certB64,
	})
	if err != nil {
		return fmt.Errorf("create HA cluster: %w", err)
	}
	if err := client.WaitSession(ctx, sess, 15*time.Second, w.cfg.SessionTimeout); err != nil {
		return fmt.Errorf("HA cluster: %w", err)
	}
	return nil
}

// compile-time assertion that *Wirer satisfies deploy.Wirer.
var _ deploy.Wirer = (*Wirer)(nil)
