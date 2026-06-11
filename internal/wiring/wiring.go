// Package wiring implements deploy.Wirer: after a topology's VMs are deployed
// and booted, it logs into the VSA's Veeam REST API and registers the other
// appliances — VIA proxy, VIA hardened repository — and, for HA topologies,
// creates the 2-node cluster. It is the orchestration layer on top of the
// internal/veeam REST client.
package wiring

import (
	"context"
	"fmt"
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
	ReadyTimeout   time.Duration // how long to wait for the VSA REST to come up
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
	if cfg.ReadyTimeout <= 0 {
		cfg.ReadyTimeout = 30 * time.Minute
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

	// Register each VIA node (proxy / hardened repo).
	for _, n := range vias {
		if n.IP == "" {
			return fmt.Errorf("node %q (%s) has no IP", n.Name, n.Role)
		}
		log(fmt.Sprintf("adding Linux host %s (%s)…", n.IP, n.Role))
		sess, err := client.AddLinuxHost(ctx, n.IP, n.Role, pairing(n), "")
		if err != nil {
			return fmt.Errorf("add host %s: %w", n.IP, err)
		}
		if err := client.WaitSession(ctx, sess, 10*time.Second, w.cfg.SessionTimeout); err != nil {
			return fmt.Errorf("add host %s: %w", n.IP, err)
		}
		hostID, err := client.FindManagedServerByName(ctx, n.IP)
		if err != nil || hostID == "" {
			return fmt.Errorf("resolve managed server %s: %v", n.IP, err)
		}

		switch {
		case isHR(n.Role):
			log(fmt.Sprintf("creating hardened repository on %s…", n.IP))
			rs, err := client.AddHardenedRepository(ctx, "HR-"+n.Name, hostID, w.cfg.RepoPath, "", true, w.cfg.ImmutableDays)
			if err != nil {
				return fmt.Errorf("add hardened repo %s: %w", n.IP, err)
			}
			if err := client.WaitSession(ctx, rs, 10*time.Second, w.cfg.SessionTimeout); err != nil {
				return fmt.Errorf("hardened repo %s: %w", n.IP, err)
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

	// HA topology: two VSA nodes → create the cluster (secondary joins primary).
	if len(vsas) >= 2 {
		if err := w.createHA(ctx, client, vsas[0], vsas[1], log); err != nil {
			return err
		}
	}
	return nil
}

// waitReady polls the VSA OAuth endpoint until authentication succeeds.
func (w *Wirer) waitReady(ctx context.Context, client *veeam.Client, log func(string)) error {
	deadline := time.Now().Add(w.cfg.ReadyTimeout)
	for {
		if err := client.Authenticate(ctx); err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("VSA REST not reachable within %s", w.cfg.ReadyTimeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
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
