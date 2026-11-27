package stellar

import (
	"fmt"

	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// ParseContractID converts a strkey contract address ("C...") into an ScAddress.
func ParseContractID(contractID string) (xdr.ScAddress, error) {
	raw, err := strkey.Decode(strkey.VersionByteContract, contractID)
	if err != nil {
		return xdr.ScAddress{}, fmt.Errorf("invalid contract id %q: %w", contractID, err)
	}
	if len(raw) != 32 {
		return xdr.ScAddress{}, fmt.Errorf("invalid contract id %q: expected 32 bytes, got %d", contractID, len(raw))
	}

	var id xdr.ContractId
	copy(id[:], raw)
	return xdr.ScAddress{
		Type:       xdr.ScAddressTypeScAddressTypeContract,
		ContractId: &id,
	}, nil
}

// InstanceKey returns the LedgerKey for a contract's instance entry.
//
// The instance is a ContractData entry with the special
// ScvLedgerKeyContractInstance key and persistent durability. It holds the
// contract's executable reference (a Wasm hash, or a built-in Stellar Asset
// Contract marker) plus its instance storage.
func InstanceKey(contract xdr.ScAddress) xdr.LedgerKey {
	return xdr.LedgerKey{
		Type: xdr.LedgerEntryTypeContractData,
		ContractData: &xdr.LedgerKeyContractData{
			Contract:   contract,
			Key:        xdr.ScVal{Type: xdr.ScValTypeScvLedgerKeyContractInstance},
			Durability: xdr.ContractDataDurabilityPersistent,
		},
	}
}

// CodeKey returns the LedgerKey for the Wasm code entry with the given hash.
func CodeKey(hash xdr.Hash) xdr.LedgerKey {
	return xdr.LedgerKey{
		Type:         xdr.LedgerEntryTypeContractCode,
		ContractCode: &xdr.LedgerKeyContractCode{Hash: hash},
	}
}

// DataKey returns the LedgerKey for a contract data entry.
func DataKey(contract xdr.ScAddress, key xdr.ScVal, durability xdr.ContractDataDurability) xdr.LedgerKey {
	return xdr.LedgerKey{
		Type: xdr.LedgerEntryTypeContractData,
		ContractData: &xdr.LedgerKeyContractData{
			Contract:   contract,
			Key:        key,
			Durability: durability,
		},
	}
}

// EncodeKey serializes a LedgerKey to the base64 form getLedgerEntries expects.
func EncodeKey(key xdr.LedgerKey) (string, error) {
	switch key.Type {
	case xdr.LedgerEntryTypeAccount:
		if key.Account == nil {
			return "", fmt.Errorf("encode ledger key: account union value is nil")
		}
	case xdr.LedgerEntryTypeTrustline:
		if key.TrustLine == nil {
			return "", fmt.Errorf("encode ledger key: trustline union value is nil")
		}
	case xdr.LedgerEntryTypeOffer:
		if key.Offer == nil {
			return "", fmt.Errorf("encode ledger key: offer union value is nil")
		}
	case xdr.LedgerEntryTypeData:
		if key.Data == nil {
			return "", fmt.Errorf("encode ledger key: data union value is nil")
		}
	case xdr.LedgerEntryTypeClaimableBalance:
		if key.ClaimableBalance == nil {
			return "", fmt.Errorf("encode ledger key: claimable balance union value is nil")
		}
	case xdr.LedgerEntryTypeLiquidityPool:
		if key.LiquidityPool == nil {
			return "", fmt.Errorf("encode ledger key: liquidity pool union value is nil")
		}
	case xdr.LedgerEntryTypeContractData:
		if key.ContractData == nil {
			return "", fmt.Errorf("encode ledger key: contract data union value is nil")
		}
	case xdr.LedgerEntryTypeContractCode:
		if key.ContractCode == nil {
			return "", fmt.Errorf("encode ledger key: contract code union value is nil")
		}
	case xdr.LedgerEntryTypeConfigSetting:
		if key.ConfigSetting == nil {
			return "", fmt.Errorf("encode ledger key: config setting union value is nil")
		}
	case xdr.LedgerEntryTypeTtl:
		if key.Ttl == nil {
			return "", fmt.Errorf("encode ledger key: ttl union value is nil")
		}
	}

	s, err := xdr.MarshalBase64(key)
	if err != nil {
		return "", fmt.Errorf("encode ledger key: %w", err)
	}
	return s, nil
}

// DecodeEntryData deserializes the base64 LedgerEntryData returned for an entry.
func DecodeEntryData(b64 string) (xdr.LedgerEntryData, error) {
	var data xdr.LedgerEntryData
	if err := xdr.SafeUnmarshalBase64(b64, &data); err != nil {
		return data, fmt.Errorf("decode ledger entry data: %w", err)
	}
	return data, nil
}
