package abi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"

	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/soroworks/soroprobe/internal/stellar"
)

const contractSpecSection = "contractspecv0"

// ChainSource reads and decodes ABI specifications from deployed Wasm code.
type ChainSource struct {
	client stellar.Client
	mu     sync.RWMutex
	cache  map[string]*Interface
}

// NewChainSource builds an ABI source backed by the given Stellar RPC client.
func NewChainSource(client stellar.Client) *ChainSource {
	return &ChainSource{client: client, cache: make(map[string]*Interface)}
}

// Interface retrieves and decodes the interface stored in the contract's Wasm.
func (s *ChainSource) Interface(ctx context.Context, _, contractID string) (*Interface, error) {
	contract, err := stellar.ParseContractID(contractID)
	if err != nil {
		return nil, err
	}
	instanceKey, err := stellar.EncodeKey(stellar.InstanceKey(contract))
	if err != nil {
		return nil, err
	}
	instanceResponse, err := s.client.GetLedgerEntries(ctx, protocol.GetLedgerEntriesRequest{Keys: []string{instanceKey}})
	if err != nil {
		return nil, err
	}
	if len(instanceResponse.Entries) == 0 {
		return nil, ErrUnknownContract
	}
	instanceData, err := stellar.DecodeEntryData(instanceResponse.Entries[0].DataXDR)
	if err != nil {
		return nil, fmt.Errorf("decode contract instance: %w", err)
	}
	if instanceData.ContractData == nil {
		return nil, errors.New("contract instance entry is not contract data")
	}
	instance, ok := instanceData.ContractData.Val.GetInstance()
	if !ok {
		return nil, errors.New("contract instance entry does not hold an instance")
	}
	if instance.Executable.Type == xdr.ContractExecutableTypeContractExecutableStellarAsset {
		return nil, ErrNoWasm
	}
	if instance.Executable.Type != xdr.ContractExecutableTypeContractExecutableWasm || instance.Executable.WasmHash == nil {
		return nil, fmt.Errorf("contract executable %s has no Wasm hash", instance.Executable.Type)
	}

	hash := fmt.Sprintf("%x", instance.Executable.WasmHash[:])
	s.mu.RLock()
	iface := s.cache[hash]
	s.mu.RUnlock()
	if iface != nil {
		return iface, nil
	}

	codeKey, err := stellar.EncodeKey(stellar.CodeKey(*instance.Executable.WasmHash))
	if err != nil {
		return nil, err
	}
	codeResponse, err := s.client.GetLedgerEntries(ctx, protocol.GetLedgerEntriesRequest{Keys: []string{codeKey}})
	if err != nil {
		return nil, err
	}
	if len(codeResponse.Entries) == 0 {
		return nil, fmt.Errorf("contract Wasm code %s was not found on ledger", hash)
	}
	codeData, err := stellar.DecodeEntryData(codeResponse.Entries[0].DataXDR)
	if err != nil {
		return nil, fmt.Errorf("decode contract Wasm entry: %w", err)
	}
	if codeData.ContractCode == nil {
		return nil, errors.New("contract code entry does not contain Wasm")
	}
	iface, err = decodeContractWasm(codeData.ContractCode.Code)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	if cached := s.cache[hash]; cached != nil {
		iface = cached
	} else {
		s.cache[hash] = iface
	}
	s.mu.Unlock()
	return iface, nil
}

func decodeContractWasm(wasm []byte) (*Interface, error) {
	section, err := customSection(wasm, contractSpecSection)
	if err != nil {
		return nil, err
	}
	reader := bytes.NewReader(section)
	iface := &Interface{}
	for reader.Len() > 0 {
		var entry xdr.ScSpecEntry
		if _, err := xdr.Unmarshal(reader, &entry); err != nil {
			return nil, fmt.Errorf("decode contract spec: %w", err)
		}
		fn, ok := entry.GetFunctionV0()
		if !ok {
			continue
		}
		function := Function{Name: string(fn.Name), Doc: fn.Doc}
		for _, input := range fn.Inputs {
			function.Inputs = append(function.Inputs, Param{Name: input.Name, Type: specType(input.Type)})
		}
		for _, output := range fn.Outputs {
			function.Outputs = append(function.Outputs, specType(output))
		}
		iface.Functions = append(iface.Functions, function)
	}
	return iface, nil
}

func customSection(wasm []byte, wanted string) ([]byte, error) {
	if len(wasm) < 8 || !bytes.Equal(wasm[:4], []byte{0x00, 'a', 's', 'm'}) {
		return nil, errors.New("invalid Wasm module")
	}
	position := 8
	for position < len(wasm) {
		sectionID := wasm[position]
		position++
		sectionSize, err := readULEB(wasm, &position)
		if err != nil || int64(sectionSize) > int64(len(wasm)-position) {
			return nil, errors.New("invalid Wasm section length")
		}
		sectionEnd := position + int(sectionSize)
		if sectionID == 0 {
			nameLength, err := readULEB(wasm[:sectionEnd], &position)
			if err != nil || int64(nameLength) > int64(sectionEnd-position) {
				return nil, errors.New("invalid Wasm custom-section name")
			}
			nameEnd := position + int(nameLength)
			name := string(wasm[position:nameEnd])
			position = nameEnd
			if name == wanted {
				return wasm[position:sectionEnd], nil
			}
		}
		position = sectionEnd
	}
	return nil, fmt.Errorf("wasm module has no %q custom section", wanted)
}

func readULEB(data []byte, position *int) (uint32, error) {
	var value uint32
	for shift := uint(0); shift < 35; shift += 7 {
		if *position >= len(data) {
			return 0, errors.New("truncated unsigned LEB128")
		}
		b := data[*position]
		*position++
		if shift == 28 && b&0xf0 != 0 {
			return 0, errors.New("unsigned LEB128 overflows uint32")
		}
		value |= uint32(b&0x7f) << shift
		if b&0x80 == 0 {
			return value, nil
		}
	}
	return 0, errors.New("unsigned LEB128 overflows uint32")
}

func specType(def xdr.ScSpecTypeDef) Type {
	switch def.Type {
	case xdr.ScSpecTypeScSpecTypeOption:
		inner := specType(def.MustOption().ValueType)
		return Type{Kind: "option", Display: "Option<" + inner.Display + ">", Inner: &inner}
	case xdr.ScSpecTypeScSpecTypeVec:
		element := specType(def.MustVec().ElementType)
		return Type{Kind: "vec", Display: "Vec<" + element.Display + ">", Element: &element}
	case xdr.ScSpecTypeScSpecTypeMap:
		mapType := def.MustMap()
		key, value := specType(mapType.KeyType), specType(mapType.ValueType)
		return Type{Kind: "map", Display: "Map<" + key.Display + ", " + value.Display + ">", Key: &key, Value: &value}
	case xdr.ScSpecTypeScSpecTypeResult:
		return Type{Kind: "result", Display: "Result"}
	case xdr.ScSpecTypeScSpecTypeTuple:
		return Type{Kind: "tuple", Display: "Tuple"}
	case xdr.ScSpecTypeScSpecTypeBytesN:
		return Type{Kind: "bytes_n", Display: "BytesN"}
	case xdr.ScSpecTypeScSpecTypeUdt:
		name := def.MustUdt().Name
		return Type{Kind: "udt", Display: name}
	}
	names := map[xdr.ScSpecType]string{
		xdr.ScSpecTypeScSpecTypeVal: "val", xdr.ScSpecTypeScSpecTypeBool: "bool",
		xdr.ScSpecTypeScSpecTypeVoid: "void", xdr.ScSpecTypeScSpecTypeError: "error",
		xdr.ScSpecTypeScSpecTypeU32: "u32", xdr.ScSpecTypeScSpecTypeI32: "i32",
		xdr.ScSpecTypeScSpecTypeU64: "u64", xdr.ScSpecTypeScSpecTypeI64: "i64",
		xdr.ScSpecTypeScSpecTypeTimepoint: "timepoint", xdr.ScSpecTypeScSpecTypeDuration: "duration",
		xdr.ScSpecTypeScSpecTypeU128: "u128", xdr.ScSpecTypeScSpecTypeI128: "i128",
		xdr.ScSpecTypeScSpecTypeU256: "u256", xdr.ScSpecTypeScSpecTypeI256: "i256",
		xdr.ScSpecTypeScSpecTypeBytes: "bytes", xdr.ScSpecTypeScSpecTypeString: "string",
		xdr.ScSpecTypeScSpecTypeSymbol: "symbol", xdr.ScSpecTypeScSpecTypeAddress: "address",
		xdr.ScSpecTypeScSpecTypeMuxedAddress: "address",
	}
	name := names[def.Type]
	if name == "" {
		name = def.Type.String()
	}
	return Type{Kind: name, Display: name}
}
