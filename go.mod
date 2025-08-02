module github.com/longcipher/pevm-go

go 1.24.2

require github.com/ethereum/go-ethereum v1.14.12

require (
	github.com/holiman/uint256 v1.3.2-0.20241012173810-555918b26655 // indirect
	golang.org/x/crypto v0.29.0 // indirect
	golang.org/x/sys v0.27.0 // indirect
)

replace github.com/ethereum/go-ethereum v1.14.12 => github.com/ethereum-optimism/op-geth v1.101411.3-rc.1
