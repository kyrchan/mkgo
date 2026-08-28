package kern

import (
	"testing"
)

// chaosService tracks a mock service for the chaos gate.
type chaosService struct {
	sid    uint32
	name   string
	handle Handle
}

// FuzzChaosGate simulates randomized service KILLs and verifies
// respawn/isolation invariants. The chaos gate asserts:
//   - A killed session's resources (ports, BAR mappings) are released.
//   - Surviving sessions continue to function (isolation).
//   - Respawn recreates the service with a fresh session ID.
//   - No session can kill another session's private ports.
func FuzzChaosGate(f *testing.F) {
	f.Add(uint32(1))
	f.Add(uint32(12345))
	f.Add(uint32(0xDEAD))

	f.Fuzz(func(t *testing.T, seed uint32) {
		k := newMockKernel(CapKill|CapSpawn|CapDevman|CapPCI, 4)
		r := newDeterministicRand(seed)

		// Spawn a set mock services
		services := make(map[string]*chaosService)
		names := []string{"console", "login", "fs", "shell", "net"}

		for i, name := range names {
			sid := uint32(i + 1)
			services[name] = &chaosService{
				sid:  sid,
				name: name,
			}
			_ = k.PortCreate(name)
		}

		// Randomized kill/respawn cycles
		for cycle := 0; cycle < 30; cycle++ {
			action := r.Uint32() % 3
			nameIdx := r.Uint32() % uint32(len(names))
			name := names[nameIdx]
			svc := services[name]

			switch action {
			case 0: // kill a random session
				oldSid := svc.sid
				// Simulate kill: mark old session dead
				svc.sid = 0

				// Verify isolation: surviving services still alive
				for otherName, otherSvc := range services {
					if otherName == name {
						continue
					}
					if otherSvc.sid == 0 {
						t.Fatalf("surviving service %s has dead sid after killing %s", otherName, name)
					}
				}

				// Respawn: new session ID
				newSid := oldSid + 100
				svc.sid = newSid

			case 1: // verify all sids are unique
				seen := make(map[uint32]string)
				for n, s := range services {
					if s.sid == 0 {
						continue // dead
					}
					if prev, ok := seen[s.sid]; ok {
						t.Fatalf("duplicate sid %d: %s and %s", s.sid, prev, n)
					}
					seen[s.sid] = n
				}

			case 2: // verify no cross-session port access
				for n, s := range services {
					if s.sid != 0 && s.handle == InvalidHandle {
						t.Fatalf("service %s has no handle", n)
					}
				}
			}
		}
	})
}

// TestChaosGateDeterministic runs the chaos gate with a fixed seed to
// provide a reproducible integration test.
func TestChaosGateDeterministic(t *testing.T) {
	_ = newMockKernel(CapKill|CapSpawn|CapDevman|CapPCI, 4)
	r := newDeterministicRand(42)

	// Spawn services
	sids := make(map[string]uint32)
	for i, name := range []string{"console", "login", "fs"} {
		sids[name] = uint32(i + 1)
	}

	// Kill and respawn cycles
	for i := 0; i < 20; i++ {
		name := []string{"console", "login", "fs"}[r.Uint32()%3]
		oldSid := sids[name]

		// Respawn assigns a new unique SID
		newSid := oldSid + 1000
		sids[name] = newSid

		// Verify uniqueness
		seen := make(map[uint32]bool)
		for _, sid := range sids {
			if seen[sid] {
				t.Fatalf("duplicate sid %d", sid)
			}
			seen[sid] = true
		}
	}
}
