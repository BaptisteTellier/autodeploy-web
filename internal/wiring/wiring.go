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
	Username       string        // VSA REST user (default "veeamadmin")
	Password       string        // VSA REST password
	Insecure       bool          // skip TLS verification (self-signed VSA cert)
	ClusterDNSName string        // HA cluster DNS name (HA topologies only)
	RepoPath       string        // hardened-repo path (default /mnt/repository)
	ImmutableDays  int           // hardened-repo immutability days (default 7)
	SessionTimeout time.Duration // how long to wait per async infra session
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
		cfg.RepoPath = "/mnt/repository"
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
			hardenedRepoID = id
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
			log("redirecting config backup to the hardened repository…")
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
	sess, err := client.CreateHACluster(ctx, veeam.HASpec{
		PrimaryNodeIP:          primary.IP,
		SecondaryNodeIP:        secondary.IP,
		SecondaryCredentialsID: credID,
		ClusterDNSName:         dns,
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
