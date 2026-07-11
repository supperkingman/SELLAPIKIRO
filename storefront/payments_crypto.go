// payments_crypto.go - BEP20 USDT (Binance Smart Chain).
// Xac minh tu dong qua BscScan API: kiem tra giao dich token USDT toi vi nhan,
// dung so tien, du confirmations. Chong double-spend qua unique(method,ref=txhash).
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"strings"
	"time"
)

// USDT BEP20 contract tren BSC (18 decimals).
const usdtBep20Contract = "0x55d398326f99059fF775485246999027B3197955"
const usdtDecimals = 18

// bscClient rieng, timeout ngan.
var bscClient = &http.Client{Timeout: 15 * time.Second}

// bscTx la 1 giao dich token tra ve tu BscScan.
type bscTx struct {
	Hash          string `json:"hash"`
	From          string `json:"from"`
	To            string `json:"to"`
	Value         string `json:"value"` // so nguyen theo decimals
	ContractAddr  string `json:"contractAddress"`
	Confirmations string `json:"confirmations"`
	TokenSymbol   string `json:"tokenSymbol"`
}

// checkCryptoPayment quet cac giao dich USDT vao vi, tim giao dich khop so tien
// don (>= pay_amount USDT) va chua duoc dung. Neu thay -> fulfill.
// Tra ve (daXacNhan, loi).
func (app *App) checkCryptoPayment(code string) (bool, error) {
	if !app.pay.CryptoEnabled() {
		return false, fmt.Errorf("crypto chua duoc cau hinh")
	}
	// Lay so tien USDT can (pay_amount) cua don.
	var payAmount float64
	var status string
	err := app.db.QueryRow(`SELECT pay_amount, status FROM orders WHERE code=?`, code).Scan(&payAmount, &status)
	if err != nil {
		return false, err
	}
	if status == "approved" {
		return true, nil
	}
	if payAmount <= 0 {
		return false, fmt.Errorf("don chua co so tien USDT")
	}

	txs, err := app.fetchUSDTTransfers()
	if err != nil {
		return false, err
	}

	wantWei := usdtToWei(payAmount)
	for _, tx := range txs {
		if !strings.EqualFold(tx.To, app.pay.USDTWallet) {
			continue
		}
		if !strings.EqualFold(tx.ContractAddr, usdtBep20Contract) {
			continue
		}
		// Du confirmations (>= 10 block ~ an toan).
		if confI, ok := new(big.Int).SetString(tx.Confirmations, 10); ok && confI.Cmp(big.NewInt(10)) < 0 {
			continue
		}
		val, ok := new(big.Int).SetString(tx.Value, 10)
		if !ok {
			continue
		}
		// So tien nhan >= so tien can (cho phep tra du).
		if val.Cmp(wantWei) < 0 {
			continue
		}
		// Khop -> fulfill (ref = tx hash chong trung).
		ref := "bsc-" + strings.ToLower(tx.Hash)
		if err := app.fulfillOrder(code, "crypto", ref, payAmount, "USDT"); err != nil {
			// co the tx da dung cho don khac -> thu tx tiep theo
			log.Printf("crypto: fulfill %s bang %s loi: %v", code, ref, err)
			continue
		}
		return true, nil
	}
	return false, nil
}

// fetchUSDTTransfers lay danh sach giao dich token USDT gan nhat toi vi.
func (app *App) fetchUSDTTransfers() ([]bscTx, error) {
	url := fmt.Sprintf(
		"https://api.bscscan.com/api?module=account&action=tokentx&contractaddress=%s&address=%s&page=1&offset=50&sort=desc&apikey=%s",
		usdtBep20Contract, app.pay.USDTWallet, app.pay.BscScanAPIKey,
	)
	resp, err := bscClient.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	var out struct {
		Status  string  `json:"status"`
		Message string  `json:"message"`
		Result  []bscTx `json:"result"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	if out.Status != "1" && out.Message != "No transactions found" {
		return nil, fmt.Errorf("bscscan: %s", out.Message)
	}
	return out.Result, nil
}

// usdtToWei doi so USDT (float) -> so nguyen 18 decimals.
func usdtToWei(amount float64) *big.Int {
	// Nhan voi 10^18 qua big.Float de tranh sai so.
	f := new(big.Float).SetFloat64(amount)
	mul := new(big.Float).SetInt(pow10(usdtDecimals))
	f.Mul(f, mul)
	wei, _ := f.Int(nil)
	return wei
}

// pow10 tra ve 10^n dang big.Int.
func pow10(n int) *big.Int {
	return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(n)), nil)
}
