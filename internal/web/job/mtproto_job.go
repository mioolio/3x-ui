package job

import (
	"math"
	"math/bits"
	"sync"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/mtproto"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

// MtprotoJob reconciles the running mtg sidecar processes against the enabled
// mtproto inbounds in the database, restarts any that crashed, and folds the
// per-client traffic scraped from each mtg /stats endpoint into the usual client
// and inbound traffic accounting.
type MtprotoJob struct {
	inboundService service.InboundService
	mu             sync.Mutex
	fractions      map[string]mtprotoBillingFraction
}

type mtprotoBillingFraction struct {
	factor    int
	remainder int64
}

// The sidecar reports authenticated user bytes, so its inbound factor can be
// billed against the real client. Carry fractional 1/10000-byte units across
// polls, just like the patched Xray dispatcher does across buffers.
func (j *MtprotoJob) billTraffic(tag, email string, physical int64, factor int) (extra, discount int64) {
	if physical <= 0 {
		return 0, 0
	}
	if factor < 100 || factor > model.MaxTrafficMultiplierBps {
		factor = model.DefaultTrafficMultiplierBps
	}
	key := tag + "\x00" + email
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.fractions == nil {
		j.fractions = make(map[string]mtprotoBillingFraction)
	}
	state := j.fractions[key]
	if state.factor != factor {
		state.remainder = 0
	}
	hi, lo := bits.Mul64(uint64(physical), uint64(factor))
	lo, carry := bits.Add64(lo, uint64(state.remainder), 0)
	hi += carry
	if hi >= 10_000 {
		return math.MaxInt64 - physical, 0
	}
	billed, remainder := bits.Div64(hi, lo, 10_000)
	if billed > math.MaxInt64 {
		return math.MaxInt64 - physical, 0
	}
	j.fractions[key] = mtprotoBillingFraction{factor: factor, remainder: int64(remainder)}
	if billed >= uint64(physical) {
		return int64(billed) - physical, 0
	}
	return 0, physical - int64(billed)
}

func mtprotoAddNonnegative(current, delta int64) int64 {
	if delta <= 0 {
		return current
	}
	if current > math.MaxInt64-delta {
		return math.MaxInt64
	}
	return current + delta
}

// trafficRows combines authenticated secrets that share one panel client row.
// The database writes client usage by email, whereas window quotas also need
// each inbound's distinct physical delta.
func (j *MtprotoJob) trafficRows(deltas []mtproto.Traffic, multipliers map[string]int, routedTags map[string]bool) ([]*xray.ClientTraffic, []*xray.InboundClientTraffic, []*xray.Traffic) {
	clientTraffics := make([]*xray.ClientTraffic, 0, len(deltas))
	clientsByEmail := make(map[string]*xray.ClientTraffic, len(deltas))
	perInboundTraffics := make([]*xray.InboundClientTraffic, 0, len(deltas))
	inboundUp := make(map[string]int64)
	inboundDown := make(map[string]int64)
	for _, d := range deltas {
		up, down := max(int64(0), d.Up), max(int64(0), d.Down)
		physical := mtprotoAddNonnegative(up, down)
		factor := multipliers[d.Tag]
		routed := routedTags[d.Tag]
		if d.RuntimePolicyKnown {
			// A stopped or edited sidecar's final sample belongs to the policy
			// under which those bytes actually flowed. Its tag may no longer be
			// present in the newly desired instance set.
			factor = d.MultiplierBps
			routed = d.RouteThroughXray
		}
		extra, discount := j.billTraffic(d.Tag, d.Email, physical, factor)
		row := clientsByEmail[d.Email]
		if row == nil {
			row = &xray.ClientTraffic{Email: d.Email}
			clientsByEmail[d.Email] = row
			clientTraffics = append(clientTraffics, row)
		}
		row.Up = mtprotoAddNonnegative(row.Up, up)
		row.Down = mtprotoAddNonnegative(row.Down, down)
		row.ChargeExtraDelta = mtprotoAddNonnegative(row.ChargeExtraDelta, extra)
		row.ChargeDiscountDelta = mtprotoAddNonnegative(row.ChargeDiscountDelta, discount)
		// Window allowances measure physical transfer, matching the patched
		// Xray dispatcher's window reservation. The multiplier only changes the
		// paid total allowance, so preserve each secret's physical delta here.
		perInboundTraffics = append(perInboundTraffics, &xray.InboundClientTraffic{
			Tag: d.Tag, Email: d.Email, Up: up, Down: down,
		})
		if !routed {
			inboundUp[d.Tag] = mtprotoAddNonnegative(inboundUp[d.Tag], up)
			inboundDown[d.Tag] = mtprotoAddNonnegative(inboundDown[d.Tag], down)
		}
	}
	traffics := make([]*xray.Traffic, 0, len(inboundUp))
	for tag, up := range inboundUp {
		traffics = append(traffics, &xray.Traffic{
			IsInbound: true, Tag: tag, Up: up, Down: inboundDown[tag],
		})
	}
	return clientTraffics, perInboundTraffics, traffics
}

// NewMtprotoJob creates a new mtproto reconcile/traffic job instance.
func NewMtprotoJob() *MtprotoJob {
	return new(MtprotoJob)
}

// Run reconciles desired mtproto inbounds with running mtg processes and records
// per-client traffic deltas and online status.
func (j *MtprotoJob) Run() {
	desired, err := j.inboundService.DesiredMtprotoInstances()
	if err != nil {
		logger.Warning("mtproto job: get desired instances failed:", err)
		return
	}

	routedTags := make(map[string]bool)
	multipliers := make(map[string]int, len(desired))
	activeTags := make([]string, 0, len(desired))
	for _, inst := range desired {
		activeTags = append(activeTags, inst.Tag)
		multipliers[inst.Tag] = inst.TrafficMultiplierBps
		if inst.RouteThroughXray {
			routedTags[inst.Tag] = true
		}
	}

	mgr := mtproto.GetManager()
	// Read the last authenticated counters before Reconcile removes exhausted
	// secrets or stops an inbound. Otherwise their final polling interval is
	// silently lost, including any higher-factor debit.
	deltas, onlineEmails := mgr.CollectTraffic()
	mgr.Reconcile(desired)
	// Prime the baseline of a newly started sidecar immediately. Otherwise its
	// first periodic scrape (up to 10s later) would treat all startup traffic
	// as pre-existing and never charge it. Any bytes from surviving processes
	// between the two scrapes still belong to this poll.
	postDeltas, postOnline := mgr.CollectTraffic()
	deltas = append(deltas, postDeltas...)
	onlineEmails = append(onlineEmails, postOnline...)

	// A routed inbound's total is already metered through the Xray bridge by
	// xray_traffic_job, so only non-routed inbounds are rolled up here; per-client
	// deltas are always kept, since the bridge cannot tell mtproto users apart.
	clientTraffics, perInboundTraffics, traffics := j.trafficRows(deltas, multipliers, routedTags)

	if len(traffics) > 0 || len(clientTraffics) > 0 {
		if _, _, err := j.inboundService.AddTraffic(traffics, clientTraffics); err != nil {
			logger.Warning("mtproto job: add traffic failed:", err)
		} else {
			xrayService := new(service.XrayService)
			if err := xrayService.RecordWindowTraffic(clientTraffics, perInboundTraffics); err != nil {
				logger.Warning("mtproto job: record window traffic failed:", err)
			} else {
				if err := xrayService.RefreshRatePolicy(); err != nil {
					logger.Warning("mtproto job: refresh rate policy failed:", err)
				}
				// The new sample may have exhausted a hard-stop window. Apply its
				// secret removal now; the normal reconcile restores it after reset.
				updated, err := j.inboundService.DesiredMtprotoInstances()
				if err != nil {
					logger.Warning("mtproto job: refresh window stops failed:", err)
				} else {
					mgr.Reconcile(updated)
					activeTags = activeTags[:0]
					for _, inst := range updated {
						activeTags = append(activeTags, inst.Tag)
					}
				}
			}
		}
	}

	j.inboundService.RefreshLocalOnlineClients(onlineEmails, activeTags)
}
