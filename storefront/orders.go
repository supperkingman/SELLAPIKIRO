// orders.go - logic fulfillment dung chung cho ca 3 cong thanh toan.
// Khi thanh toan da xac nhan -> tao key qua keyadmin (kem han) -> luu -> approved.
package main

import (
	"fmt"
	"log"
	"sync"
	"time"
)

// fulfillMu tuan tu hoa viec fulfill de tranh race (2 webhook cung 1 don).
var fulfillMu sync.Mutex

// orderForFulfill gom du lieu can de fulfill 1 don.
type orderForFulfill struct {
	ID          int64
	Code        string
	UserID      int64
	Status      string
	Credits     int
	PackageName string
	Email       string
	DurDays     int
	DurHours    int
	RenewKeyID  int64 // != 0 -> don gia han key cu thay vi tao key moi
}

// loadOrderForFulfill lay du lieu don + goi (kem thoi han).
func (app *App) loadOrderForFulfill(code string) (orderForFulfill, error) {
	var o orderForFulfill
	err := app.db.QueryRow(
		`SELECT o.id,o.code,o.user_id,o.status,o.credits,p.name,u.email,
		        p.duration_days,p.duration_hours,o.renew_key_id
		 FROM orders o
		 JOIN packages p ON p.id=o.package_id
		 JOIN users u ON u.id=o.user_id
		 WHERE o.code=?`, code,
	).Scan(&o.ID, &o.Code, &o.UserID, &o.Status, &o.Credits, &o.PackageName, &o.Email, &o.DurDays, &o.DurHours, &o.RenewKeyID)
	return o, err
}

// fulfillOrder tao key cho don (idempotent: neu da approved thi bo qua).
// method: sepay|crypto|paypal. ref: tx_hash|paypal_id|sepay_id (chong trung).
// amount/currency: de luu vao bang payments.
func (app *App) fulfillOrder(code, method, ref string, amount float64, currency string) error {
	fulfillMu.Lock()
	defer fulfillMu.Unlock()

	o, err := app.loadOrderForFulfill(code)
	if err != nil {
		return fmt.Errorf("khong tim thay don %s: %w", code, err)
	}
	if o.Status == "approved" {
		return nil // da xu ly roi
	}

	// Ghi nhan payment (unique index method+ref chong double-spend).
	now := time.Now().Unix()
	if ref != "" {
		_, err := app.db.Exec(
			`INSERT INTO payments(order_id,method,amount,currency,ref,status,created_at,confirmed_at)
			 VALUES(?,?,?,?,?, 'confirmed', ?, ?)`,
			o.ID, method, amount, currency, ref, now, now,
		)
		if err != nil {
			// Vi phong unique -> giao dich nay da dung cho don khac.
			return fmt.Errorf("giao dich %s da duoc su dung: %w", ref, err)
		}
	}

	// Don gia han: cong credit + thoi gian cua goi vao key cu, khong tao key moi.
	if o.RenewKeyID != 0 {
		if err := app.renewExistingKey(o); err != nil {
			return fmt.Errorf("gia han key that bai: %w", err)
		}
		if _, err := app.db.Exec(
			`UPDATE orders SET status='approved', approved_at=?, pay_method=? WHERE id=?`,
			now, method, o.ID,
		); err != nil {
			return err
		}
		log.Printf("order %s (renewal) fulfilled via %s", code, method)
		return nil
	}

	// Tao key qua keyadmin, kem thoi han theo goi.
	keyName := fmt.Sprintf("%s | %s | %s", o.Code, o.PackageName, o.Email)
	created, err := app.ka.CreateKeyWithExpiry(keyName, o.Credits, o.DurDays, o.DurHours)
	if err != nil {
		return fmt.Errorf("tao key that bai: %w", err)
	}
	kiroID := app.lookupKeyID(created.Name)

	if _, err := app.db.Exec(
		`INSERT INTO issued_keys(order_id,user_id,kiro_key_id,api_key,credit_limit,created_at)
		 VALUES(?,?,?,?,?,?)`,
		o.ID, o.UserID, kiroID, created.Key, o.Credits, now,
	); err != nil {
		return fmt.Errorf("luu key that bai: %w", err)
	}

	if _, err := app.db.Exec(
		`UPDATE orders SET status='approved', approved_at=?, pay_method=? WHERE id=?`,
		now, method, o.ID,
	); err != nil {
		return err
	}
	log.Printf("order %s fulfilled via %s", code, method)
	return nil
}

// renewExistingKey cong credit + thoi gian cua goi vao key da mua (don gia han).
func (app *App) renewExistingKey(o orderForFulfill) error {
	var kiroID string
	var creditLimit int
	if err := app.db.QueryRow(
		`SELECT kiro_key_id, credit_limit FROM issued_keys WHERE id=? AND user_id=?`,
		o.RenewKeyID, o.UserID,
	).Scan(&kiroID, &creditLimit); err != nil {
		return fmt.Errorf("khong tim thay key gia han: %w", err)
	}
	if kiroID == "" {
		return fmt.Errorf("key gia han chua lien ket kiro-go")
	}
	// Cong credit cua goi.
	if o.Credits > 0 {
		if err := app.ka.AddCredit(kiroID, float64(o.Credits)); err != nil {
			return err
		}
		_, _ = app.db.Exec(
			`UPDATE issued_keys SET credit_limit=credit_limit+? WHERE id=?`,
			o.Credits, o.RenewKeyID,
		)
	}
	// Cong thoi gian cua goi (days + hours).
	if o.DurDays > 0 || o.DurHours > 0 {
		if err := app.ka.AddHours(kiroID, o.DurDays*24+o.DurHours); err != nil {
			return err
		}
	}
	return nil
}
