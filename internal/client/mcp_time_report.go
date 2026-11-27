package client

import (
	"context"
	"encoding/json"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/hazcod/blur/internal/activity"
	"github.com/hazcod/blur/internal/appidentity"
	"github.com/hazcod/blur/internal/assistant"
	"github.com/hazcod/blur/internal/localapi"
)

const (
	maxMCPReportDays       = 400
	maxMCPReportBuckets    = 15
	maxMCPReportDomains    = 5
	maxMCPReportTop        = 20
	maxMCPReportTopSources = 8
	mcpReportNote          = "Tracked foreground screen time computed from Blur's saved activity segments for this period. It is not a timesheet: idle time, time away from this computer, and time in untracked apps are not included. Blur's own interface is excluded."
)

type mcpTimeReportParams struct {
	Period string `json:"period,omitempty"`
	From   string `json:"from,omitempty"`
	To     string `json:"to,omitempty"`
}

type mcpReportDomain struct {
	Domain  string `json:"domain"`
	Seconds int64  `json:"seconds"`
}

type mcpReportBucket struct {
	AppID   string            `json:"app_id"`
	Name    string            `json:"name"`
	Seconds int64             `json:"seconds"`
	ByDay   map[string]int64  `json:"by_day"`
	Domains []mcpReportDomain `json:"domains,omitempty"`
}

type mcpReportTop struct {
	AppID     string   `json:"app_id,omitempty"`
	Domain    string   `json:"domain,omitempty"`
	Title     string   `json:"title,omitempty"`
	Seconds   int64    `json:"seconds"`
	SourceIDs []string `json:"source_ids"`
}

type mcpTimeReportResponse struct {
	From                   string            `json:"from"`
	To                     string            `json:"to"`
	TotalSeconds           int64             `json:"total_seconds"`
	Buckets                []mcpReportBucket `json:"buckets"`
	OmittedBuckets         int               `json:"omitted_buckets,omitempty"`
	OmittedBucketSeconds   int64             `json:"omitted_bucket_seconds,omitempty"`
	TopSegments            []mcpReportTop    `json:"top_segments"`
	OmittedSegmentsSeconds int64             `json:"omitted_segments_seconds"`
	Note                   string            `json:"note"`
}

var mcpReportPeriods = map[string]bool{"today": true, "yesterday": true, "this_week": true, "last_week": true, "last_7_days": true, "last_30_days": true}

func decodeMCPTimeReportParams(raw []byte) (mcpTimeReportParams, error) {
	var params mcpTimeReportParams
	if err := decodeMCPParityParams(raw, &params); err != nil {
		return mcpTimeReportParams{}, err
	}
	custom := params.From != "" || params.To != ""
	switch {
	case params.Period != "" && custom:
		return mcpTimeReportParams{}, ErrInvalidRequest
	case params.Period != "":
		if !mcpReportPeriods[params.Period] {
			return mcpTimeReportParams{}, ErrInvalidRequest
		}
	case custom:
		if _, err := time.Parse("2006-01-02", params.From); err != nil {
			return mcpTimeReportParams{}, ErrInvalidRequest
		}
		if _, err := time.Parse("2006-01-02", params.To); err != nil {
			return mcpTimeReportParams{}, ErrInvalidRequest
		}
	default:
		params.Period = "today"
	}
	return params, nil
}

// mcpReportRange returns the half-open local range [start, end) for a request.
func mcpReportRange(params mcpTimeReportParams, now time.Time, zone *time.Location) (time.Time, time.Time, error) {
	day := func(offset int) time.Time {
		return activity.StartOfDate(now.Year(), now.Month(), now.Day()+offset, zone)
	}
	sinceMonday := (int(now.Weekday()) + 6) % 7
	switch params.Period {
	case "today":
		return day(0), day(1), nil
	case "yesterday":
		return day(-1), day(0), nil
	case "this_week":
		return day(-sinceMonday), day(-sinceMonday + 7), nil
	case "last_week":
		return day(-sinceMonday - 7), day(-sinceMonday), nil
	case "last_7_days":
		return day(-6), day(1), nil
	case "last_30_days":
		return day(-29), day(1), nil
	}
	from, err := time.Parse("2006-01-02", params.From)
	if err != nil {
		return time.Time{}, time.Time{}, ErrInvalidRequest
	}
	to, err := time.Parse("2006-01-02", params.To)
	if err != nil || to.Before(from) || to.Sub(from) > time.Duration(maxMCPReportDays-1)*24*time.Hour {
		return time.Time{}, time.Time{}, ErrInvalidRequest
	}
	return activity.StartOfDate(from.Year(), from.Month(), from.Day(), zone), activity.StartOfDate(to.Year(), to.Month(), to.Day()+1, zone), nil
}

func reportSeconds(ns int64) int64 { return (ns + int64(time.Second)/2) / int64(time.Second) }

type reportBucketAcc struct {
	app     string
	total   int64
	days    map[string]int64
	domains map[string]int64
}

type reportTopAcc struct {
	app, domain, title string
	total              int64
	sources            []string
}

func (c *Client) readMCPTimeReport(ctx context.Context, epoch uint64, params mcpTimeReportParams) (json.RawMessage, error) {
	_, ok := c.data.(ActivityData)
	if !ok {
		return nil, ErrNotConfigured
	}
	zone, err := loadZone(localZoneName())
	if err != nil {
		return nil, ErrInvalidRequest
	}
	start, end, err := mcpReportRange(params, c.askClock().In(zone), zone)
	if err != nil {
		return nil, err
	}
	buckets, tops := map[string]*reportBucketAcc{}, map[string]*reportTopAcc{}
	var total int64
	metadataEntries := 0
	admit := func() error {
		if metadataEntries >= maxActivityScopeMetadata {
			return ErrActivityLimit
		}
		metadataEntries++
		return nil
	}
	read, err := c.walkActivityScope(ctx, start, end, assistant.Plan{}, func(rows []activity.Segment) error {
		for _, row := range rows {
			if err := ctx.Err(); err != nil {
				return err
			}
			if row.Validate() != nil {
				return ErrInvalidRequest
			}
			if strings.EqualFold(row.AppID, appidentity.BundleID) || !row.WallEnd.After(start) || !row.WallStart.Before(end) {
				continue
			}
			from, to := row.WallStart, row.WallEnd
			if from.Before(start) {
				from = start
			}
			if to.After(end) {
				to = end
			}
			duration := int64(to.Sub(from))
			if duration <= 0 {
				continue
			}
			total += duration
			bucket := buckets[strings.ToLower(row.AppID)]
			if bucket == nil {
				if err := admit(); err != nil {
					return err
				}
				bucket = &reportBucketAcc{app: row.AppID, days: map[string]int64{}, domains: map[string]int64{}}
				buckets[strings.ToLower(row.AppID)] = bucket
			}
			bucket.total += duration
			if row.Domain != "" && appidentity.IsBrowserApp(strings.ToLower(row.AppID)) {
				if _, found := bucket.domains[row.Domain]; !found {
					if err := admit(); err != nil {
						return err
					}
				}
				bucket.domains[row.Domain] += duration
			}
			row.WallStart, row.WallEnd, row.Duration = from, to, duration
			days, err := activity.SplitDays(row, zone)
			if err != nil {
				return err
			}
			for _, day := range days {
				if _, found := bucket.days[day.Date]; !found {
					if err := admit(); err != nil {
						return err
					}
				}
				bucket.days[day.Date] += day.Duration
			}
			title := row.Topic
			if title == "" {
				title = row.Project
			}
			key := strings.ToLower(row.AppID) + "\x00" + strings.ToLower(row.Domain) + "\x00" + title
			top := tops[key]
			if top == nil {
				if err := admit(); err != nil {
					return err
				}
				top = &reportTopAcc{app: row.AppID, domain: row.Domain, title: title}
				tops[key] = top
			}
			top.total += duration
			for _, id := range row.SourceIDs {
				if len(top.sources) < maxMCPReportTopSources && validMCPSourceID(id) && !slices.Contains(top.sources, id) {
					if err := admit(); err != nil {
						return err
					}
					top.sources = append(top.sources, id)
				}
			}
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	response := mcpTimeReportResponse{
		From: start.Format("2006-01-02"), To: end.Add(-time.Nanosecond).In(zone).Format("2006-01-02"),
		TotalSeconds: reportSeconds(total), Buckets: []mcpReportBucket{}, TopSegments: []mcpReportTop{}, Note: mcpReportNote,
	}
	bucketList := make([]*reportBucketAcc, 0, len(buckets))
	for _, bucket := range buckets {
		bucketList = append(bucketList, bucket)
	}
	sort.Slice(bucketList, func(i, j int) bool {
		if bucketList[i].total != bucketList[j].total {
			return bucketList[i].total > bucketList[j].total
		}
		return strings.ToLower(bucketList[i].app) < strings.ToLower(bucketList[j].app)
	})
	for i, bucket := range bucketList {
		if i >= maxMCPReportBuckets {
			response.OmittedBuckets++
			response.OmittedBucketSeconds += reportSeconds(bucket.total)
			continue
		}
		item := mcpReportBucket{AppID: bucket.app, Name: reportAppName(bucket.app), Seconds: reportSeconds(bucket.total), ByDay: map[string]int64{}}
		for day, ns := range bucket.days {
			if seconds := reportSeconds(ns); seconds > 0 {
				item.ByDay[day] = seconds
			}
		}
		domains := make([]string, 0, len(bucket.domains))
		for domain := range bucket.domains {
			domains = append(domains, domain)
		}
		sort.Slice(domains, func(a, b int) bool {
			if bucket.domains[domains[a]] != bucket.domains[domains[b]] {
				return bucket.domains[domains[a]] > bucket.domains[domains[b]]
			}
			return domains[a] < domains[b]
		})
		for _, domain := range domains[:min(maxMCPReportDomains, len(domains))] {
			item.Domains = append(item.Domains, mcpReportDomain{Domain: domain, Seconds: reportSeconds(bucket.domains[domain])})
		}
		response.Buckets = append(response.Buckets, item)
	}
	topList := make([]*reportTopAcc, 0, len(tops))
	for _, top := range tops {
		topList = append(topList, top)
	}
	sort.Slice(topList, func(i, j int) bool {
		if topList[i].total != topList[j].total {
			return topList[i].total > topList[j].total
		}
		left, right := strings.ToLower(topList[i].app+"\x00"+topList[i].domain+"\x00"+topList[i].title), strings.ToLower(topList[j].app+"\x00"+topList[j].domain+"\x00"+topList[j].title)
		return left < right
	})
	var shown int64
	for _, top := range topList[:min(maxMCPReportTop, len(topList))] {
		sources := append([]string{}, top.sources...)
		sort.Strings(sources)
		response.TopSegments = append(response.TopSegments, mcpReportTop{AppID: top.app, Domain: top.domain, Title: top.title, Seconds: reportSeconds(top.total), SourceIDs: sources})
		shown += top.total
	}
	response.OmittedSegmentsSeconds = max(0, reportSeconds(total)-reportSeconds(shown))
	for !mcpParityFits(response, localapi.MaxRPCSnapshotResponseBytes) {
		switch {
		case len(response.TopSegments) > 5:
			last := response.TopSegments[len(response.TopSegments)-1]
			response.OmittedSegmentsSeconds += last.Seconds
			response.TopSegments = response.TopSegments[:len(response.TopSegments)-1]
		case len(response.Buckets) > 1:
			last := response.Buckets[len(response.Buckets)-1]
			response.OmittedBuckets++
			response.OmittedBucketSeconds += last.Seconds
			response.Buckets = response.Buckets[:len(response.Buckets)-1]
		default:
			return nil, ErrInvalidRequest
		}
	}
	raw, err := c.publishMCPParity(ctx, epoch, response, localapi.MaxRPCSnapshotResponseBytes)
	if err != nil {
		return nil, err
	}
	if !read.publish(c, func() {}) {
		return nil, ErrMemoryChanging
	}
	return raw, nil
}

func reportAppName(id string) string {
	if id == "" {
		return "Unknown app"
	}
	if installed, ok := appDisplayName(id); ok {
		return installed
	}
	return displayAppName(id)
}
