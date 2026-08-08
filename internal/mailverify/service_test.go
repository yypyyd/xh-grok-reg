package mailverify

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"xh-grok-reg/internal/mailfetch"
	"xh-grok-reg/internal/models"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

type fakeVerifier struct {
	mu    sync.Mutex
	calls map[string]int
	fail  map[string]bool
}

func (f *fakeVerifier) Verify(_ context.Context, acc mailfetch.Account) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[acc.Email]++
	if f.fail[acc.Email] {
		return errors.New("invalid credentials")
	}
	return nil
}

func testDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.Mailbox{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func waitStatus(t *testing.T, db *gorm.DB, id uint, want string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var mailbox models.Mailbox
		if err := db.First(&mailbox, id).Error; err == nil && mailbox.Status == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("mailbox %d did not reach status %q", id, want)
}

func TestServiceRecoversAndProcessesPersistedJobs(t *testing.T) {
	db := testDB(t)
	mailboxes := []models.Mailbox{
		{Email: "ok@example.com", ClientID: "client", RefreshToken: "ok", Status: "unverified"},
		{Email: "bad@example.com", ClientID: "client", RefreshToken: "bad", Status: "verifying"},
	}
	if err := db.Create(&mailboxes).Error; err != nil {
		t.Fatal(err)
	}
	fake := &fakeVerifier{calls: map[string]int{}, fail: map[string]bool{"bad@example.com": true}}
	service := New(db, fake, 2)
	if err := service.Start(); err != nil {
		t.Fatal(err)
	}
	defer service.Stop()

	waitStatus(t, db, mailboxes[0].ID, "verified")
	waitStatus(t, db, mailboxes[1].ID, "verify_failed")
	var failed models.Mailbox
	if err := db.First(&failed, mailboxes[1].ID).Error; err != nil {
		t.Fatal(err)
	}
	if failed.VerifyError != "invalid credentials" {
		t.Fatalf("verify_error=%q want invalid credentials", failed.VerifyError)
	}
}

func TestReauthenticateAllQueuesCompletedRows(t *testing.T) {
	db := testDB(t)
	mailbox := models.Mailbox{Email: "again@example.com", ClientID: "client", RefreshToken: "token", Status: "verified"}
	if err := db.Create(&mailbox).Error; err != nil {
		t.Fatal(err)
	}
	fake := &fakeVerifier{calls: map[string]int{}, fail: map[string]bool{}}
	service := New(db, fake, 1)
	if err := service.Start(); err != nil {
		t.Fatal(err)
	}
	defer service.Stop()

	queued, err := service.ReauthenticateAll()
	if err != nil || queued != 1 {
		t.Fatalf("queued=%d err=%v", queued, err)
	}
	waitStatus(t, db, mailbox.ID, "verified")
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.calls[mailbox.Email] != 1 {
		t.Fatalf("calls=%d want 1", fake.calls[mailbox.Email])
	}
}

func TestReauthenticateFailedQueuesOnlyFailedRows(t *testing.T) {
	db := testDB(t)
	mailboxes := []models.Mailbox{
		{Email: "failed@example.com", Status: "verify_failed", VerifyError: "expired"},
		{Email: "verified@example.com", Status: "verified"},
		{Email: "pending@example.com", Status: "unverified"},
	}
	if err := db.Create(&mailboxes).Error; err != nil {
		t.Fatal(err)
	}
	service := New(db, &fakeVerifier{calls: map[string]int{}, fail: map[string]bool{}}, 1)

	queued, err := service.ReauthenticateFailed()
	if err != nil || queued != 1 {
		t.Fatalf("queued=%d err=%v", queued, err)
	}

	want := map[uint]string{
		mailboxes[0].ID: "unverified",
		mailboxes[1].ID: "verified",
		mailboxes[2].ID: "unverified",
	}
	for id, status := range want {
		var mailbox models.Mailbox
		if err := db.First(&mailbox, id).Error; err != nil {
			t.Fatal(err)
		}
		if mailbox.Status != status {
			t.Fatalf("mailbox %d status=%q want %q", id, mailbox.Status, status)
		}
		if id == mailboxes[0].ID && mailbox.VerifyError != "" {
			t.Fatalf("mailbox %d verify_error=%q want empty", id, mailbox.VerifyError)
		}
	}
}
