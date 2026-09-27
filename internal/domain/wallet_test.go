package domain

import (
	"errors"
	"testing"
	"time"
)

func TestWalletInvariants(t *testing.T) {
	now := time.Now().UTC()
	initial, _ := ParseMoney("100", "BRL")
	bet, _ := ParseMoney("80", "BRL")
	w, e := NewWallet("0192f291-27dd-7d3f-8071-5f8685deef37", "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1", initial, now)
	if e != nil {
		t.Fatal(e)
	}
	if e = w.Debit(bet, now); e != nil {
		t.Fatal(e)
	}
	if w.Balance().Amount() != "20.00" || w.Version() != 2 {
		t.Fatal(w)
	}
	before := w
	if e = w.Debit(bet, now); !errors.Is(e, ErrInsufficientFunds) || w != before {
		t.Fatal(w, e)
	}
	zero, _ := Zero("BRL")
	if e = w.Credit(zero, now.Add(time.Second)); e != nil || w != before {
		t.Fatal(w, e)
	}
	usd, _ := Zero("USD")
	if e = w.Credit(usd, now); !errors.Is(e, ErrCurrency) {
		t.Fatal(e)
	}
	restored, e := RestoreWallet(w.ID(), w.PlayerID(), w.Balance(), w.Version(), w.CreatedAt(), w.UpdatedAt())
	if e != nil || restored != w {
		t.Fatal(restored, e)
	}
	var invalid Wallet
	if invalid.Credit(initial, now) == nil {
		t.Fatal("accepted uninitialized wallet")
	}
}
func TestOperationNormalization(t *testing.T) {
	m, _ := ParseMoney("025.0", "BRL")
	o := Operation{ProviderID: "provider-a", ExternalID: "t1", PlayerID: "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1", WalletID: "0192f291-27dd-7d3f-8071-5f8685deef37", RoundID: "r", GameID: "g", Kind: Bet, Money: m}
	h, e := o.Hash()
	if e != nil {
		t.Fatal(e)
	}
	o.Money, _ = ParseMoney("25.00", "BRL")
	other, _ := o.Hash()
	if h != other {
		t.Fatal("normalization changed hash")
	}
	o.GameID = "other"
	other, _ = o.Hash()
	if h == other {
		t.Fatal("payload conflict undetected")
	}
	for _, kind := range []Kind{Bet, Win, Refund, Rollback} {
		o.Kind = kind
		o.Reference = "original"
		o.Money, _ = Zero("BRL")
		if o.Validate() == nil {
			t.Fatal("zero accepted", kind)
		}
	}
	o.Kind = Loss
	o.Reference = ""
	if o.Validate() != nil {
		t.Fatal("LOSS zero rejected")
	}
	o.Kind = Opening
	if o.Validate() == nil {
		t.Fatal("external opening accepted")
	}
}

func TestSelfReferenceRejected(t *testing.T) {
	m, _ := ParseMoney("1", "BRL")
	o := Operation{ProviderID: "provider-a", ExternalID: "self", PlayerID: "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1", WalletID: "0192f291-27dd-7d3f-8071-5f8685deef37", RoundID: "r", GameID: "g", Kind: Win, Money: m, Reference: "self"}
	if !errors.Is(o.Validate(), ErrInvalidOperation) {
		t.Fatal("self-reference accepted")
	}
}
