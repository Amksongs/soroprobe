package probe

import (
	"context"
	"fmt"
)

// NetworkResult reports RPC server health and network configuration details.
type NetworkResult struct {
	RPCURL            string `json:"rpc_url"`
	Status            string `json:"status"`
	LatestLedger      uint32 `json:"latest_ledger"`
	ProtocolVersion   int    `json:"protocol_version"`
	NetworkPassphrase string `json:"network_passphrase"`
	PassphraseMatch   bool   `json:"passphrase_match"`
}

// Network queries the configured RPC endpoint for its health status and network details.
func (p *Prober) Network(ctx context.Context, rpcURL, configuredPassphrase string) (*NetworkResult, error) {
	health, err := p.client.GetHealth(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: getHealth: %w", ErrRPCUnreachable, err)
	}

	netResp, err := p.client.GetNetwork(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: getNetwork: %w", ErrRPCUnreachable, err)
	}

	return &NetworkResult{
		RPCURL:            rpcURL,
		Status:            health.Status,
		LatestLedger:      health.LatestLedger,
		ProtocolVersion:   netResp.ProtocolVersion,
		NetworkPassphrase: netResp.Passphrase,
		PassphraseMatch:   netResp.Passphrase == configuredPassphrase,
	}, nil
}
