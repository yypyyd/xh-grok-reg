package grokproducer

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"xh-grok-reg/internal/models"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

var testDBSequence uint64

func testProducer(t *testing.T) (*Producer, *gorm.DB) {
	t.Helper()
	dsn := fmt.Sprintf("file:%s_%d?mode=memory&cache=shared",
		strings.ReplaceAll(t.Name(), "/", "_"), atomic.AddUint64(&testDBSequence, 1))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err = db.AutoMigrate(&models.Mailbox{}, &models.GrokRegistration{}, &models.Setting{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return New(db, nil), db
}

func TestClaimTargetsRetriesOnlyCooledDownFailures(t *testing.T) {
	producer, db := testProducer(t)
	mailboxes := []models.Mailbox{
		{Email: "old-failure@example.com", Status: "verified"},
		{Email: "recent-failure@example.com", Status: "verified"},
		{Email: "existing@example.com", Status: "verified"},
	}
	if err := db.Create(&mailboxes).Error; err != nil {
		t.Fatal(err)
	}
	registrations := []models.GrokRegistration{
		{Email: mailboxes[0].Email, MailboxID: mailboxes[0].ID, Status: "register_failed"},
		{Email: mailboxes[1].Email, MailboxID: mailboxes[1].ID, Status: "register_failed"},
		{Email: mailboxes[2].Email, MailboxID: mailboxes[2].ID, Status: "already_registered"},
	}
	if err := db.Create(&registrations).Error; err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := db.Model(&registrations[0]).UpdateColumn("updated_at", old).Error; err != nil {
		t.Fatal(err)
	}

	claimed, cooling, err := producer.claimTargets(3)
	if err != nil {
		t.Fatal(err)
	}
	if cooling || len(claimed) != 1 || claimed[0].Email != mailboxes[0].Email {
		t.Fatalf("unexpected claim: cooling=%v claimed=%+v", cooling, claimed)
	}
	if claimed[0].Status != "registering" || claimed[0].Password == "" {
		t.Fatalf("claimed record was not reset: %+v", claimed[0])
	}

	claimed, cooling, err = producer.claimTargets(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 0 || !cooling {
		t.Fatalf("recent failure should report cooling: cooling=%v claimed=%+v", cooling, claimed)
	}
}

func TestTargetRemainingCountsOnlyTrackedSuccesses(t *testing.T) {
	producer, db := testProducer(t)
	regs := []models.GrokRegistration{
		{Email: "one@example.com", Status: "registered"},
		{Email: "two@example.com", Status: "register_failed"},
		{Email: "outside@example.com", Status: "registered"},
	}
	if err := db.Create(&regs).Error; err != nil {
		t.Fatal(err)
	}
	_, topUpID, ok := producer.beginTopUp()
	if !ok || !producer.beginRun(topUpID, 3, regs[:2]) {
		t.Fatal("could not initialize tracked batch")
	}
	defer producer.endTopUp(topUpID)
	if got := producer.targetRemaining(1); got != 1 {
		t.Fatalf("remaining=%d, want 1", got)
	}
	producer.cancel[regs[1].ID] = func() {}
	producer.cancel[regs[2].ID] = func() {}
	if got := producer.batchRunningNum(); got != 1 {
		t.Fatalf("batch running=%d, want 1; unrelated manual task must be excluded", got)
	}
	snapshot := producer.Snapshot()
	if snapshot.Registered != 1 || snapshot.Failed != 1 || snapshot.Target != 3 {
		t.Fatalf("snapshot counted records outside the current run: %+v", snapshot)
	}
}

func TestStaleTopUpCleanupDoesNotCancelNewBatch(t *testing.T) {
	producer, _ := testProducer(t)
	firstCtx, firstID, ok := producer.beginTopUp()
	if !ok {
		t.Fatal("first batch was not started")
	}
	producer.stopTopUp()
	select {
	case <-firstCtx.Done():
	default:
		t.Fatal("stopped batch context was not canceled")
	}

	secondCtx, secondID, ok := producer.beginTopUp()
	if !ok || secondID == firstID {
		t.Fatalf("second batch did not receive a new generation: first=%d second=%d", firstID, secondID)
	}
	producer.endTopUp(firstID)
	select {
	case <-secondCtx.Done():
		t.Fatal("stale cleanup canceled the new batch")
	default:
	}
	if producer.topUpCancel == nil {
		t.Fatal("stale cleanup cleared the new batch")
	}
	newRegs := []models.GrokRegistration{{ID: 20}}
	if !producer.beginRun(secondID, 1, newRegs) {
		t.Fatal("new batch could not initialize its progress")
	}
	if producer.beginRun(firstID, 99, []models.GrokRegistration{{ID: 10}}) {
		t.Fatal("stale batch overwrote the new batch progress")
	}
	if producer.trackRun(firstID, []models.GrokRegistration{{ID: 11}}) {
		t.Fatal("stale batch appended to the new batch progress")
	}
	if producer.runTarget != 1 || len(producer.runTracked) != 1 || producer.runTracked[0] != 20 {
		t.Fatalf("new batch progress was changed by stale work: target=%d tracked=%v", producer.runTarget, producer.runTracked)
	}

	producer.endTopUp(secondID)
	if secondCtx.Err() != context.Canceled {
		t.Fatalf("second batch cleanup did not cancel its context: %v", secondCtx.Err())
	}
}
