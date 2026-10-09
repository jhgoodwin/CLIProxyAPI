//go:build linux && cgo

package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sirupsen/logrus"
)

func runCLI(args []string) error {
	if len(args) < 3 || args[0] != "--state" {
		return errors.New("usage: tenant-policy --state /absolute/path init|account-set|user-set|user-share|user-credit-share|quota-observe|credits-observe|quota-reset|user-disable|user-enable|user-clear-overage|key-create|key-list|key-label|key-revoke|metrics-export|dashboard|status")
	}
	path := args[1]
	if !filepath.IsAbs(path) {
		return errors.New("--state must be an absolute path")
	}
	cmd, a := args[2], args[3:]
	if cmd == "metrics-export" || cmd == "dashboard" {
		if (cmd == "metrics-export" && len(a) != 2) || (cmd == "dashboard" && len(a) != 1) {
			return errors.New("usage: metrics-export json|csv OUTPUT_PATH | dashboard OUTPUT_HTML_PATH")
		}
		return withLedger(path, false, func(l *Ledger) error {
			if cmd == "dashboard" {
				return writeReport(a[0], func(w io.Writer) error { return renderDashboard(w, l) })
			}
			return writeReport(a[1], func(w io.Writer) error { return exportMetrics(w, l, a[0]) })
		})
	}
	if cmd == "init" {
		if len(a) != 0 {
			return errors.New("usage: init")
		}
		dir := filepath.Dir(path)
		if err := os.MkdirAll(dir, 0700); err != nil {
			return err
		}
		dirInfo, err := os.Lstat(dir)
		if err != nil {
			return err
		}
		if dirInfo.Mode()&os.ModeSymlink != 0 || !dirInfo.IsDir() {
			return errors.New("ledger parent must be a real directory")
		}
		if dirInfo.Mode().Perm()&0077 != 0 {
			return errors.New("ledger parent permissions must be 0700")
		}
		// Publish only a fully written ledger, and never replace an existing file.
		f, err := os.CreateTemp(dir, ".tenant-ledger-init-*")
		if err != nil {
			return err
		}
		tmpPath := f.Name()
		defer func() {
			if removeErr := os.Remove(tmpPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
				logrus.Errorf("remove temporary ledger initialization: %v", removeErr)
			}
		}()
		b, _ := json.MarshalIndent(emptyLedger(), "", "  ")
		if err = f.Chmod(0600); err == nil {
			_, err = f.Write(append(b, '\n'))
		}
		if err == nil {
			err = f.Sync()
		}
		closeErr := f.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			return err
		}
		if err = os.Link(tmpPath, path); err != nil {
			return err
		}
		d, err := os.Open(dir)
		if err != nil {
			return err
		}
		defer func() {
			if closeErr := d.Close(); closeErr != nil {
				logrus.Errorf("close ledger directory: %v", closeErr)
			}
		}()
		return d.Sync()
	}
	var createdToken string
	readOnly := cmd == "status" || cmd == "key-list"
	err := withLedger(path, !readOnly, func(l *Ledger) error {
		switch cmd {
		case "account-set":
			return setAccountAlias(l, a)
		case "quota-reset":
			return resetQuota(l, a)
		case "credits-observe":
			return observeCredits(l, a)
		case "quota-observe":
			return observeQuota(l, a)
		case "user-set":
			if len(a) < 3 {
				return errors.New("usage: user-set ID LIMIT_USD PROVIDER...")
			}
			if strings.TrimSpace(a[0]) != a[0] || a[0] == "" {
				return errors.New("invalid user ID")
			}
			limit, err := usd(a[1])
			if err != nil {
				return err
			}
			providers := make([]string, 0, len(a)-2)
			seen := map[string]bool{}
			for _, p := range a[2:] {
				if strings.TrimSpace(p) != p || p == "" {
					return errors.New("invalid provider")
				}
				if !seen[p] {
					providers = append(providers, p)
					seen[p] = true
				}
			}
			old := l.Users[a[0]]
			spent := old.SpentUSD
			if spent == "" {
				spent = "0"
			}
			if _, err := usd(spent); err != nil {
				return fmt.Errorf("existing user spent amount: %w", err)
			}
			l.Users[a[0]] = User{Providers: providers, PoolShares: old.PoolShares, CreditShares: old.CreditShares, LimitUSD: usdString(limit), SpentUSD: spent, Disabled: old.Disabled, OverageBlocked: old.OverageBlocked}
			return nil
		case "user-share", "user-credit-share":
			if len(a) != 3 {
				return errors.New("usage: user-share USER PROVIDER PERCENT")
			}
			u, ok := l.Users[a[0]]
			if !ok {
				return errors.New("unknown user")
			}
			share, err := poolPercent(a[2])
			if err != nil {
				return err
			}
			if cmd == "user-credit-share" {
				if u.CreditShares == nil {
					u.CreditShares = map[string]string{}
				}
				u.CreditShares[a[1]] = usdString(share)
				l.Users[a[0]] = u
				return nil
			}
			if u.PoolShares == nil {
				u.PoolShares = make(map[string]string)
			}
			u.PoolShares[a[1]] = usdString(share)
			l.Users[a[0]] = u
			return nil
		case "user-disable", "user-enable":
			if len(a) != 1 {
				return errors.New("usage: user-disable ID")
			}
			u, ok := l.Users[a[0]]
			if !ok {
				return errors.New("unknown user")
			}
			u.Disabled = cmd == "user-disable"
			l.Users[a[0]] = u
			return nil
		case "user-clear-overage":
			if len(a) != 1 {
				return errors.New("usage: user-clear-overage ID (admin acknowledgment; ensure the user's allowances have been corrected)")
			}
			u, ok := l.Users[a[0]]
			if !ok {
				return errors.New("unknown user")
			}
			u.OverageBlocked = false
			l.Users[a[0]] = u
			return nil
		case "key-create":
			if len(a) < 1 || len(a) > 2 {
				return errors.New("usage: key-create USER [LABEL]")
			}
			if _, ok := l.Users[a[0]]; !ok {
				return errors.New("unknown user")
			}
			secret := make([]byte, 32)
			if _, err := rand.Read(secret); err != nil {
				return err
			}
			createdToken = hex.EncodeToString(secret)
			label := ""
			if len(a) == 2 {
				label = a[1]
			}
			l.Keys[KeyHash(createdToken)] = Key{User: a[0], Label: label}
			return nil
		case "key-list":
			if len(a) != 0 {
				return errors.New("usage: key-list")
			}
			type keyRow struct {
				Hash    string `json:"hash"`
				Label   string `json:"label"`
				User    string `json:"user"`
				Revoked bool   `json:"revoked"`
			}
			rows := make([]keyRow, 0, len(l.Keys))
			for hash, key := range l.Keys {
				rows = append(rows, keyRow{Hash: hash, User: key.User, Label: key.Label, Revoked: key.Revoked})
			}
			sort.Slice(rows, func(i, j int) bool { return rows[i].Hash < rows[j].Hash })
			return json.NewEncoder(os.Stdout).Encode(rows)
		case "key-label":
			if len(a) != 2 {
				return errors.New("usage: key-label HASH LABEL (empty label clears the note)")
			}
			k, ok := l.Keys[a[0]]
			if !ok {
				return errors.New("unknown key hash")
			}
			k.Label = a[1]
			l.Keys[a[0]] = k
			return nil
		case "key-revoke":
			if len(a) != 1 {
				return errors.New("usage: key-revoke HASH")
			}
			k, ok := l.Keys[a[0]]
			if !ok {
				return errors.New("unknown key hash")
			}
			k.Revoked = true
			l.Keys[a[0]] = k
			return nil
		case "status":
			if len(a) != 0 {
				return errors.New("usage: status")
			}
			type row struct {
				ID string `json:"id"`
				User
			}
			rows := make([]row, 0, len(l.Users))
			for id, u := range l.Users {
				rows = append(rows, row{id, u})
			}
			sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
			return json.NewEncoder(os.Stdout).Encode(rows)
		default:
			return fmt.Errorf("unknown command %q", cmd)
		}
	})
	if err != nil {
		return err
	}
	if createdToken != "" {
		fmt.Println(createdToken)
	}
	return nil
}
