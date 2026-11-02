package testdata

import "embed"

//go:embed erc20.wasm
//go:embed inc_func.wasm
//go:embed sum_func.wasm
//go:embed test-cases.wasm
//go:embed shared-fungible-token-wallet.wasm
//go:embed frozen_transfer.wasm
var content embed.FS

func Erc20() ([]byte, error) {
	return content.ReadFile("erc20.wasm")
}

func IncFunc() ([]byte, error) {
	return content.ReadFile("inc_func.wasm")
}

func SumFunc() ([]byte, error) {
	return content.ReadFile("sum_func.wasm")
}

func TestCases() ([]byte, error) {
	return content.ReadFile("test-cases.wasm")
}

// FrozenTransfer is a contract whose "send"(to, amount) makes one iDNA transfer promise (frozen_transfer.wat).
func FrozenTransfer() ([]byte, error) {
	return content.ReadFile("frozen_transfer.wasm")
}

func SharedFungibleToken() ([]byte, error) {
	return content.ReadFile("shared-fungible-token-wallet.wasm")
}
