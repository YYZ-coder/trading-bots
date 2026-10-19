package hyperliquid

import (
	"bytes"
	"crypto/ecdsa"
	"encoding/binary"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/vmihailenco/msgpack/v5"
)

// Hyperliquid 的 L1 交易签名流程（和官方 Python SDK 的 sign_l1_action 一致）：
//
//  1. connectionId = keccak256( msgpack(action) || nonce(8 字节大端) || 0x00 )
//     最后的 0x00 表示没有使用 vault（子账户/金库）
//  2. 构造 EIP-712 结构体 Agent{source, connectionId}，主网 source="a"，测试网 "b"
//  3. 用 domain {name:"Exchange", version:"1", chainId:1337, verifyingContract:0x0} 签名
//
// 注意：msgpack 的字段顺序必须和官方一致，所以 action 一律用带 msgpack tag 的结构体，
// 不能用 map（map 的遍历顺序是随机的）。

type signature struct {
	R string `json:"r"`
	S string `json:"s"`
	V byte   `json:"v"`
}

var (
	eip712DomainTypeHash = crypto.Keccak256([]byte("EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)"))
	agentTypeHash        = crypto.Keccak256([]byte("Agent(string source,bytes32 connectionId)"))
	domainSeparator      = crypto.Keccak256(
		eip712DomainTypeHash,
		crypto.Keccak256([]byte("Exchange")),
		crypto.Keccak256([]byte("1")),
		common32(big.NewInt(1337).Bytes()),
		make([]byte, 32), // verifyingContract = 0x000...0
	)
)

// common32 把字节左侧补零到 32 字节（EIP-712 里 uint256 的编码方式）。
func common32(b []byte) []byte {
	out := make([]byte, 32)
	copy(out[32-len(b):], b)
	return out
}

// actionHash 计算 connectionId。
func actionHash(action any, nonce int64) ([]byte, error) {
	var buf bytes.Buffer
	if err := msgpack.NewEncoder(&buf).Encode(action); err != nil {
		return nil, fmt.Errorf("msgpack encode: %w", err)
	}
	buf.Write(binary.BigEndian.AppendUint64(nil, uint64(nonce)))
	buf.WriteByte(0x00)
	return crypto.Keccak256(buf.Bytes()), nil
}

// signL1Action 对交易 action 签名。
func signL1Action(key *ecdsa.PrivateKey, action any, nonce int64, testnet bool) (signature, error) {
	connID, err := actionHash(action, nonce)
	if err != nil {
		return signature{}, err
	}
	source := "a"
	if testnet {
		source = "b"
	}
	structHash := crypto.Keccak256(agentTypeHash, crypto.Keccak256([]byte(source)), connID)
	digest := crypto.Keccak256([]byte{0x19, 0x01}, domainSeparator, structHash)

	sig, err := crypto.Sign(digest, key) // 65 字节：R(32) || S(32) || V(1)，V 是 0 或 1
	if err != nil {
		return signature{}, err
	}
	return signature{
		R: hexutil.EncodeBig(new(big.Int).SetBytes(sig[:32])),
		S: hexutil.EncodeBig(new(big.Int).SetBytes(sig[32:64])),
		V: sig[64] + 27,
	}, nil
}
