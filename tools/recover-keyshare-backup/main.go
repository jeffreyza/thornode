package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"

	"github.com/cosmos/cosmos-sdk/types"
	"github.com/itchio/lzma"
	"github.com/rs/zerolog/log"

	"gitlab.com/thorchain/thornode/v3/bifrost/tss"
	"gitlab.com/thorchain/thornode/v3/cmd"
	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"
	"gitlab.com/thorchain/thornode/v3/tools/thorscan"
	"gitlab.com/thorchain/thornode/v3/x/thorchain"
)

////////////////////////////////////////////////////////////////////////////////////////
// Helpers
////////////////////////////////////////////////////////////////////////////////////////

func check(e error, msg string) {
	if e != nil {
		_, file, line, _ := runtime.Caller(1)
		callerLine := fmt.Sprintf("%s:%d", file, line)
		log.Fatal().Msgf("%s: %s\n%s", callerLine, msg, e)
	}
}

func get(url string, result interface{}) error {
	// make the request
	res, err := http.DefaultClient.Get(url)
	if err != nil {
		return err
	}

	// check the status code
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: status code %d", url, res.StatusCode)
	}

	// populate the result
	defer res.Body.Close()
	return json.NewDecoder(res.Body).Decode(result)
}

func selectMember(members []string) (string, error) {
	if len(members) == 0 {
		return "", fmt.Errorf("no options available")
	}

	// display the options
	fmt.Println("Select vault member:")
	for i, option := range members {
		fmt.Printf("%d. %s\n", i+1, option)
	}

	// read user input
	reader := bufio.NewReader(os.Stdin)
	fmt.Print("Enter the number of member: ")
	input, err := reader.ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("error reading input: %v", err)
	}

	// convert input to integer
	input = strings.TrimSpace(input)
	choice, err := strconv.Atoi(input)
	if err != nil || choice < 1 || choice > len(members) {
		return "", fmt.Errorf("invalid choice, please enter a number between 1 and %d", len(members))
	}

	// return the selected option
	return members[choice-1], nil
}

////////////////////////////////////////////////////////////////////////////////////////
// Main
////////////////////////////////////////////////////////////////////////////////////////

func main() {
	// configure prefixes
	cfg := types.GetConfig()
	cfg.SetBech32PrefixForAccount(cmd.Bech32PrefixAccAddr, cmd.Bech32PrefixAccPub)
	cfg.SetBech32PrefixForValidator(cmd.Bech32PrefixValAddr, cmd.Bech32PrefixValPub)
	cfg.SetBech32PrefixForConsensusNode(cmd.Bech32PrefixConsAddr, cmd.Bech32PrefixConsPub)
	cfg.SetCoinType(cmd.THORChainCoinType)
	cfg.SetPurpose(cmd.THORChainCoinPurpose)
	cfg.Seal()

	// prompt for thornode endpoint
	reader := bufio.NewReader(os.Stdin)
	defaultEndpoint := "https://gateway.liquify.com/chain/thorchain_api"
	fmt.Printf("Thornode (must contain vault block heights) [%s]: ", defaultEndpoint)
	thornode, err := reader.ReadString('\n')
	check(err, "Failed to read endpoint")
	thornode = strings.TrimSpace(thornode)
	thorscan.APIEndpoint = thornode

	// prompt for vault
	fmt.Print("Vault: ")
	vault, err := reader.ReadString('\n')
	check(err, "Failed to read vault")
	vault = strings.TrimSpace(vault)

	// get vault response
	vaultResponse := openapi.Vault{}
	vaultUrl := fmt.Sprintf("%s/thorchain/vault/%s", thornode, vault)
	err = get(vaultUrl, &vaultResponse)
	check(err, "Failed to get vault")

	// get nodes at vault height
	nodes := []openapi.Node{}
	nodesUrl := fmt.Sprintf("%s/thorchain/nodes?height=%d", thornode, *vaultResponse.StatusSince)
	err = get(nodesUrl, &nodes)
	check(err, "Failed to get nodes")

	// filter node addresses that are members
	memberAddresses := []string{}
	for _, node := range nodes {
		for _, member := range node.SignerMembership {
			if member == vault {
				memberAddresses = append(memberAddresses, node.NodeAddress)
				break
			}
		}
	}

	// select a node
	selectedNode, err := selectMember(memberAddresses)
	check(err, "Failed to select node")

	// scan from 20 blocks before vault block height to find corresponding TssPool
	var keyshare []byte
	var keyshareEddsa []byte
	start := *vaultResponse.BlockHeight - 20
	stop := *vaultResponse.BlockHeight
	for block := range thorscan.Scan(int(start), int(stop)) {
		for _, tx := range block.Txs {
			for _, msg := range tx.Tx.GetMsgs() {
				msgTssPool, ok := msg.(*thorchain.MsgTssPool)
				if !ok {
					continue
				}
				if msgTssPool.Signer.String() != selectedNode {
					continue
				}

				// error if keyshare is missing
				if msgTssPool.KeysharesBackup == nil {
					log.Fatal().Msg("Keyshare was not backed up.")
				}
				keyshare = msgTssPool.KeysharesBackup
				keyshareEddsa = msgTssPool.KeysharesBackupEddsa
				break
			}
		}
		if keyshare != nil {
			break
		}
	}

	// prompt for mnemonic
	fmt.Print("Mnemonic: ")
	mnemonic, err := reader.ReadString('\n')
	check(err, "Failed to read mnemonic")
	mnemonic = strings.TrimSpace(mnemonic)

	// decrypt keyshare
	decrypted, err := tss.DecryptKeyshares(keyshare, mnemonic)
	check(err, "Failed to decrypt keyshare")

	// decompress lzma
	cmpDec := lzma.NewReader(bytes.NewReader(decrypted))

	// write to file
	filename := fmt.Sprintf("localstate-%s.json", vault)
	f, err := os.OpenFile(filename, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	check(err, "Failed to open file")
	defer f.Close()
	_, err = io.Copy(f, cmpDec)
	check(err, "Failed to write to file")

	// success
	fmt.Printf("Decrypted keyshare written to %s\n", filename)

	if len(keyshareEddsa) > 0 {
		// decrypt eddsa keyshare
		decryptedEddsa, err := tss.DecryptKeyshares(keyshareEddsa, mnemonic)
		check(err, "Failed to decrypt keyshare")

		cmpDecEddsa := lzma.NewReader(bytes.NewReader(decryptedEddsa))

		filenameEddsa := fmt.Sprintf("localstate-%s.json", *vaultResponse.PubKeyEddsa)
		fed, err := os.OpenFile(filenameEddsa, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		check(err, "Failed to open file")
		defer fed.Close()
		_, err = io.Copy(fed, cmpDecEddsa)
		check(err, "Failed to write to file")

		// success
		fmt.Printf("Decrypted eddsa keyshare written to %s\n", filenameEddsa)
	}
}
