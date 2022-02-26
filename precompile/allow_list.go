// (c) 2019-2020, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package precompile

import (
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/log"
)

// Singleton StatefulPrecompiledContracts for W/R access to the contract deployer allow list.
var (
	ModifyAllowListPrecompile StatefulPrecompiledContract = &modifyAllowListPrecompile{}
	ReadAllowListPrecompile   StatefulPrecompiledContract = &readAllowListPrecompile{}
)

type AllowListRole common.Hash

// Enum constants for valid AllowListRole
var (
	None     AllowListRole = AllowListRole(common.Hash{})  // No role assigned - this is equivalent to common.Hash{} and deletes the key from the DB when set
	Deployer AllowListRole = AllowListRole(common.Hash{1}) // Deployers are allowed to create new contracts
	Admin    AllowListRole = AllowListRole(common.Hash{2}) // Admin - allowed to modify both the admin and deployer list as well as deploy contracts
)

const (
	modifyAllowListInputLength = common.AddressLength + common.HashLength // Required length of an input to modify allow list precompile
)

// AllowListConfig specifies the configuration of the allow list.
// Specifies the block timestamp at which it goes into effect as well as the initial set of allow list admins.
type AllowListConfig struct {
	BlockTimestamp *big.Int `json:"blockTimestamp"`

	AllowListAdmins []common.Address `json:"adminAddresses"`
}

// Timestamp returns the timestamp at which the allow list should be enabled
func (c *AllowListConfig) Timestamp() *big.Int { return c.BlockTimestamp }

// Configure initializes the address space of [ModifyAllowListAddress] by initializing the role of each of
// the addresses in [AllowListAdmins].
func (c *AllowListConfig) Configure(state StateDB) {
	for _, adminAddr := range c.AllowListAdmins {
		state.SetState(ModifyAllowListAddress, CreateAddressKey(adminAddr), common.Hash(Admin))
	}
}

// Valid returns true iff [s] represents a valid role.
func (s AllowListRole) Valid() bool {
	switch s {
	case None, Deployer, Admin:
		return true
	default:
		return false
	}
}

// IsAdmin returns true if [s] indicates the permission to modify the allow list.
func (s AllowListRole) IsAdmin() bool {
	switch s {
	case Admin:
		return true
	default:
		return false
	}
}

// HasDeployerPrivileges returns true iff [s] indicates the permission to deploy contracts.
func (s AllowListRole) CanDeploy() bool {
	switch s {
	case Deployer, Admin:
		return true
	default:
		return false
	}
}

// GetAllowListStatus returns the allow list role of [address].
func GetAllowListStatus(state StateDB, address common.Address) AllowListRole {
	stateSlot := CreateAddressKey(address)
	res := state.GetState(ModifyAllowListAddress, stateSlot)
	return AllowListRole(res)
}

// PackModifyAllowList packs the arguments [address] and [status] into a single byte slice as input to the modify allow list
// precompile
func PackModifyAllowList(address common.Address, status AllowListRole) ([]byte, error) {
	// If [allow], set the last byte to 1 to indicate that [address] should be added to the whitelist
	if !status.Valid() {
		return nil, fmt.Errorf("unexpected status: %d", status)
	}

	input := make([]byte, modifyAllowListInputLength)
	copy(input, address[:])
	copy(input[common.AddressLength:], status[:])
	return input, nil
}

// UnpackModifyAllowList attempts to unpack [input] into the arguments to the allow list precompile
// verifies that the provided arguments are valid.
func UnpackModifyAllowList(input []byte) (common.Address, common.Hash, error) {
	if len(input) != modifyAllowListInputLength {
		return common.Address{}, common.Hash{}, fmt.Errorf("unexpected address length: %d not %d", len(input), modifyAllowListInputLength)
	}

	address := common.BytesToAddress(input[:common.AddressLength])
	statusHash := common.BytesToHash(input[common.AddressLength:])
	status := AllowListRole(statusHash)

	if !status.Valid() {
		return common.Address{}, common.Hash{}, fmt.Errorf("invalid status used as input for allow list precompile: %v", status)
	}

	return address, statusHash, nil
}

// setAllowListStatus sets the permissions of [address] to [status]
// assumes [status] has already been verified as valid.
func setAllowListStatus(stateDB StateDB, address common.Address, status common.Hash) {
	// Generate the state key for [address]
	addressKey := CreateAddressKey(address)
	log.Info("modify allow list", "address", address, "role", status)
	// Assign [role] to the address
	stateDB.SetState(ModifyAllowListAddress, addressKey, status)
}

// modifyAllowListPrecompile implements StatefulPrecompiledContract and can be used as a thread-safe singleton.
// Provides designated admins to modify the contract deployers allow list.
type modifyAllowListPrecompile struct{}

// Run verifies that [callerAddr] has the correct permissions to modify the allow list and if so updates the the allow list
// as requested by the arguments encoded in [input].
func (mal *modifyAllowListPrecompile) Run(evm PrecompileAccessibleState, callerAddr common.Address, addr common.Address, input []byte, suppliedGas uint64, readOnly bool) (ret []byte, remainingGas uint64, err error) {
	// Note: this should never happen since the required gas should be verified before calling Run.
	if suppliedGas < ModifyAllowListGasCost {
		return nil, 0, fmt.Errorf("running allow list exceeds gas allowance (%d) < (%d)", ModifyAllowListGasCost, suppliedGas)
	}

	remainingGas = suppliedGas - ModifyAllowListGasCost
	if readOnly {
		return nil, remainingGas, fmt.Errorf("cannot modify allow list in read only")
	}

	// Verify that the caller is in the allow list and therefore has the right to modify it
	callerStatus := GetAllowListStatus(evm.GetStateDB(), callerAddr)
	if !callerStatus.IsAdmin() {
		log.Info("EVM received attempt to modify the allow list from a non-allowed address", "callerAddr", callerAddr)
		return nil, remainingGas, fmt.Errorf("caller %s cannot modify allow list", callerAddr)
	}

	// Unpack the precompile's input into the arguments for setAllowListStatus
	address, status, err := UnpackModifyAllowList(input)
	if err != nil {
		log.Info("modify allow list reverted", "err", err)
		return nil, remainingGas, fmt.Errorf("failed to unpack modify allow list input: %w", err)
	}

	setAllowListStatus(evm.GetStateDB(), address, status)

	// Return an empty output and the remaining gas
	return []byte{}, remainingGas, nil
}

// RequiredGas returns the amount of gas consumed by this precompile.
func (mal *modifyAllowListPrecompile) RequiredGas(input []byte) uint64 { return ModifyAllowListGasCost }

// readAllowListPrecompile implements StatefulPrecompiledContract and can be used as a thread-safe singleton.
// Provides read access to the contract deployer allow list.
type readAllowListPrecompile struct{}

// Run implements StatefulPrecompiledContract
// parses [input] into a single address and returns the 32 byte hash that specifies the designated role of that address.
func (ral *readAllowListPrecompile) Run(evm PrecompileAccessibleState, callerAddr common.Address, addr common.Address, input []byte, suppliedGas uint64, readOnly bool) (ret []byte, remainingGas uint64, err error) {
	// Note: this should never happen since the required gas should be verified before calling Run.
	if suppliedGas < ReadAllowListGasCost {
		return nil, 0, fmt.Errorf("running allow list exceeds gas allowance (%d) < (%d)", ReadAllowListGasCost, suppliedGas)
	}

	remainingGas = suppliedGas - ReadAllowListGasCost

	if len(input) != common.AddressLength {
		return nil, remainingGas, fmt.Errorf("invalid input length for read allow list: %d", len(input))
	}

	readAddress := common.BytesToAddress(input)
	roleBytes := common.Hash(GetAllowListStatus(evm.GetStateDB(), readAddress)).Bytes()
	return roleBytes, remainingGas, nil
}

// RequiredGas returns the amount of gas consumed by this precompile.
func (mal *readAllowListPrecompile) RequiredGas(input []byte) uint64 { return ReadAllowListGasCost }