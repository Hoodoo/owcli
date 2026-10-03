package claims

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/Hoodoo/owcli/internal/evidence"
)

// NewClaimID returns "claim_" plus 32 random hex digits.
func NewClaimID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("claims: random id: %v", err))
	}
	return "claim_" + hex.EncodeToString(b[:])
}

// Apply runs a batch of operations against a page's complete Claim set as
// one transaction: nothing changes unless every operation validates and all
// evidence resolves. newID allocates ids for added Claims. The input slice is
// not modified.
func Apply(claims []Claim, ops []Operation, resolver evidence.Resolver, newID func() string) ([]Claim, error) {
	if len(ops) == 0 {
		return nil, invalidf("a claim mutation requires at least one operation")
	}
	if err := validateClaims(claims); err != nil {
		return nil, invalidf("%v", err)
	}
	existing := map[string]bool{}
	for _, c := range claims {
		existing[c.ID] = true
	}
	targeted := map[string]bool{}
	for _, op := range ops {
		if err := validateOperation(op); err != nil {
			return nil, err
		}
		if op.Op == OpAdd {
			continue
		}
		if targeted[op.ID] {
			return nil, invalidf("claim %s is targeted more than once in one batch", op.ID)
		}
		targeted[op.ID] = true
		if !existing[op.ID] {
			return nil, invalidf("unknown claim id: %s", op.ID)
		}
	}

	resolver = evidence.Cached(resolver)
	working := cloneClaims(claims)
	byID := func(id string) int {
		for i, c := range working {
			if c.ID == id {
				return i
			}
		}
		return -1
	}

	// Resolve everything before changing anything.
	resolved := make([][]evidence.Evidence, len(ops))
	for i, op := range ops {
		var err error
		switch op.Op {
		case OpAdd:
			resolved[i], err = resolveAll(proposed(op.Evidence), resolver)
		case OpConfirm, OpUpdate:
			current := working[byID(op.ID)]
			inputs := current.Evidence
			if op.Op == OpUpdate && op.Evidence != nil {
				inputs = proposed(op.Evidence)
			}
			resolved[i], err = resolveAll(inputs, resolver)
		}
		if err != nil {
			return nil, err
		}
	}

	for i, op := range ops {
		switch op.Op {
		case OpAdd:
			id, err := uniqueID(existing, newID)
			if err != nil {
				return nil, err
			}
			working = append(working, Claim{ID: id, Statement: strings.TrimSpace(*op.Statement), Evidence: resolved[i]})
		case OpRetract:
			j := byID(op.ID)
			working = append(working[:j], working[j+1:]...)
		default:
			j := byID(op.ID)
			if op.Op == OpUpdate && op.Statement != nil {
				working[j].Statement = strings.TrimSpace(*op.Statement)
			}
			working[j].Evidence = resolved[i]
		}
	}
	return cloneClaims(working), nil
}

func proposed(resources []string) []evidence.Evidence {
	out := make([]evidence.Evidence, len(resources))
	for i, r := range resources {
		out[i] = evidence.Evidence{Resource: r}
	}
	return out
}

// resolveAll resolves inputs, using each one's version as the relocation hint.
func resolveAll(inputs []evidence.Evidence, r evidence.Resolver) ([]evidence.Evidence, error) {
	out := make([]evidence.Evidence, 0, len(inputs))
	seenIn, seenOut := map[string]bool{}, map[string]bool{}
	for _, in := range inputs {
		if seenIn[in.Resource] {
			return nil, invalidf("claim evidence repeats %s", in.Resource)
		}
		seenIn[in.Resource] = true
		res, err := r.Resolve(in.Resource, in.Version)
		if errors.Is(err, evidence.ErrInvalidResource) || errors.Is(err, evidence.ErrSecurity) {
			return nil, invalidf("%v", err)
		}
		if err != nil {
			return nil, err
		}
		if res == nil {
			return nil, invalidf("evidence does not resolve: %s", in.Resource)
		}
		if seenOut[res.Evidence.Resource] {
			return nil, invalidf("claim evidence resolves to duplicate resource %s", res.Evidence.Resource)
		}
		seenOut[res.Evidence.Resource] = true
		out = append(out, res.Evidence)
	}
	return out, nil
}

func validateOperation(op Operation) error {
	switch op.Op {
	case OpConfirm, OpRetract:
		return canonicalInput(op.ID, string(op.Op)+" claim id")
	case OpUpdate:
		if err := canonicalInput(op.ID, "update claim id"); err != nil {
			return err
		}
		if op.Statement == nil && op.Evidence == nil {
			return invalidf("an update requires a statement or evidence change")
		}
	case OpAdd:
		if op.Statement == nil {
			return invalidf("an added claim requires a statement")
		}
		if op.Evidence == nil {
			return invalidf("a claim requires at least one evidence resource")
		}
	default:
		return invalidf("unsupported claim operation %q", op.Op)
	}
	if op.Statement != nil && strings.TrimSpace(*op.Statement) == "" {
		return invalidf("claim statement cannot be empty")
	}
	if op.Evidence != nil && len(op.Evidence) == 0 {
		return invalidf("a claim requires at least one evidence resource")
	}
	for _, r := range op.Evidence {
		if err := canonicalInput(r, "proposed evidence resource"); err != nil {
			return err
		}
	}
	return nil
}

func canonicalInput(v, label string) error {
	if err := canonical(v, label); err != nil {
		return invalidf("%v", err)
	}
	return nil
}

func uniqueID(reserved map[string]bool, newID func() string) (string, error) {
	for i := 0; i < 10; i++ {
		id := newID()
		if err := canonicalInput(id, "generated claim id"); err != nil {
			return "", err
		}
		if !reserved[id] {
			reserved[id] = true
			return id, nil
		}
	}
	return "", invalidf("unable to allocate a unique claim identifier")
}
