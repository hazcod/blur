package durable

import (
	"context"
	"encoding/binary"
	"errors"
	"time"
)

const (
	retentionPolicyNamespace = "privacy"
	retentionPolicyID        = "retention-policy-v1"
	retentionPolicyBytes     = 12
	DefaultRetentionDays     = 90
)

var ErrInvalidRetentionPolicy = errors.New("durable store: invalid retention policy")

// RetentionPolicy is encrypted with the durable store. An absent policy uses
// DefaultRetentionDays; Days=0 is an explicit choice to disable automatic
// age-based deletion. LastSweepAt is zero until a sweep succeeds.
type RetentionPolicy struct {
	Days        int
	LastSweepAt time.Time
}

func ValidRetentionDays(days int) bool {
	switch days {
	case 0, 30, 90, 180, 365:
		return true
	default:
		return false
	}
}

func (s *Store) GetRetentionPolicy(ctx context.Context) (RetentionPolicy, error) {
	encoded, found, err := s.readIndexValue(ctx, retentionPolicyNamespace, retentionPolicyID, retentionPolicyBytes)
	if err != nil {
		return RetentionPolicy{}, err
	}
	if !found {
		return RetentionPolicy{Days: DefaultRetentionDays}, nil
	}
	defer clear(encoded)
	return decodeRetentionPolicy(encoded)
}

func (s *Store) SetRetentionDays(ctx context.Context, days int) error {
	if !ValidRetentionDays(days) || ctx == nil {
		return ErrInvalidRetentionPolicy
	}
	return s.Update(ctx, func(tx *Tx) error {
		policy, err := tx.readRetentionPolicy()
		if err != nil {
			return err
		}
		if policy.Days == days {
			return nil
		}
		policy.Days = days
		policy.LastSweepAt = time.Time{}
		return tx.PutIndexRecord(retentionPolicyNamespace, retentionPolicyID, encodeRetentionPolicy(policy))
	})
}

func (s *Store) MarkRetentionSweep(ctx context.Context, at time.Time) error {
	if ctx == nil || at.IsZero() || !validCaptureTime(at) {
		return ErrInvalidRetentionPolicy
	}
	return s.Update(ctx, func(tx *Tx) error {
		policy, err := tx.readRetentionPolicy()
		if err != nil {
			return err
		}
		if policy.Days == 0 {
			return ErrInvalidRetentionPolicy
		}
		policy.LastSweepAt = at.UTC()
		return tx.PutIndexRecord(retentionPolicyNamespace, retentionPolicyID, encodeRetentionPolicy(policy))
	})
}

func (tx *Tx) readRetentionPolicy() (RetentionPolicy, error) {
	encoded, found, err := tx.readIndexValue(retentionPolicyNamespace, retentionPolicyID, retentionPolicyBytes)
	if err != nil {
		return RetentionPolicy{}, err
	}
	if !found {
		return RetentionPolicy{Days: DefaultRetentionDays}, nil
	}
	defer clear(encoded)
	return decodeRetentionPolicy(encoded)
}

func encodeRetentionPolicy(policy RetentionPolicy) []byte {
	encoded := make([]byte, retentionPolicyBytes)
	encoded[0] = 1
	binary.BigEndian.PutUint16(encoded[1:3], uint16(policy.Days))
	if !policy.LastSweepAt.IsZero() {
		encoded[3] = 1
		binary.BigEndian.PutUint64(encoded[4:12], uint64(policy.LastSweepAt.UTC().UnixNano()))
	}
	return encoded
}

func decodeRetentionPolicy(encoded []byte) (RetentionPolicy, error) {
	if len(encoded) != retentionPolicyBytes || encoded[0] != 1 || encoded[3] > 1 {
		return RetentionPolicy{}, ErrCorrupt
	}
	days := int(binary.BigEndian.Uint16(encoded[1:3]))
	if !ValidRetentionDays(days) {
		return RetentionPolicy{}, ErrCorrupt
	}
	policy := RetentionPolicy{Days: days}
	if encoded[3] == 1 {
		policy.LastSweepAt = time.Unix(0, int64(binary.BigEndian.Uint64(encoded[4:12]))).UTC()
		if !validCaptureTime(policy.LastSweepAt) || days == 0 {
			return RetentionPolicy{}, ErrCorrupt
		}
	} else if binary.BigEndian.Uint64(encoded[4:12]) != 0 {
		return RetentionPolicy{}, ErrCorrupt
	}
	return policy, nil
}
