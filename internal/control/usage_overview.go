package control

import (
	"context"
	"sort"
	"strconv"

	"github.com/gin-gonic/gin"

	app_errors "gpt-load/internal/platform/errors"
	"gpt-load/internal/platform/response"
	"gpt-load/internal/requestlog"
)

// 每人累计用量总览端点(阶段 4): 数据源 usage_stats(不随日志保留清理),
// 支持全历史。筛选: from_ms/to_ms(可单边)、group_id、access_key_id。

type usageOverviewPerson struct {
	AccessKeyID uint                            `json:"access_key_id"`
	Name        string                          `json:"name"`
	Totals      requestlog.AccessKeyUsageTotals `json:"totals"`
}

type usageOverviewCredential struct {
	CredentialID uint                            `json:"credential_id"`
	Name         string                          `json:"name"`
	Totals       requestlog.AccessKeyUsageTotals `json:"totals"`
}

type usageOverviewResponse struct {
	ObservedAtMS int64                            `json:"observed_at_ms"`
	Filter       usageOverviewFilterResponse      `json:"filter"`
	People       []usageOverviewPerson            `json:"people"`
	Credentials  []usageOverviewCredential        `json:"credentials"`
	Series       []requestlog.AccessKeyUsagePoint `json:"series"`
}

type usageOverviewFilterResponse struct {
	FromMS      int64 `json:"from_ms"`
	ToMS        int64 `json:"to_ms"`
	GroupID     uint  `json:"group_id"`
	AccessKeyID uint  `json:"access_key_id"`
}

func (server *Server) handleUsageOverview(c *gin.Context) {
	reader, ok := server.service.usageStats.(usageOverviewReader)
	if !ok {
		writeServiceError(c, "usage_overview", app_errors.ErrInternalServer)
		return
	}
	filter := requestlog.AccessKeyOverviewFilter{
		FromMS:      parseOverviewMS(c.Query("from_ms")),
		ToMS:        parseOverviewMS(c.Query("to_ms")),
		GroupID:     parseOverviewID(c.Query("group_id")),
		AccessKeyID: parseOverviewID(c.Query("access_key_id")),
	}
	overview, err := reader.AccessKeyUsageOverview(c.Request.Context(), filter)
	if err != nil {
		writeServiceError(c, "usage_overview", err)
		return
	}
	names, err := server.service.accessKeyNames(c)
	if err != nil {
		writeServiceError(c, "usage_overview", err)
		return
	}
	credentialNames, err := server.service.credentialNames(c)
	if err != nil {
		writeServiceError(c, "usage_overview", err)
		return
	}
	result := usageOverviewResponse{
		ObservedAtMS: overview.ObservedAtMS,
		Filter: usageOverviewFilterResponse{
			FromMS: overview.Filter.FromMS, ToMS: overview.Filter.ToMS,
			GroupID: overview.Filter.GroupID, AccessKeyID: overview.Filter.AccessKeyID,
		},
		People:      make([]usageOverviewPerson, 0, len(overview.Totals)),
		Credentials: make([]usageOverviewCredential, 0, len(overview.Credentials)),
		Series:      overview.Series,
	}
	for id, totals := range overview.Totals {
		result.People = append(result.People, usageOverviewPerson{
			AccessKeyID: id, Name: names[id], Totals: totals,
		})
	}
	sortPeople(result.People)
	for id, totals := range overview.Credentials {
		result.Credentials = append(result.Credentials, usageOverviewCredential{
			CredentialID: id, Name: credentialNames[id], Totals: totals,
		})
	}
	sort.Slice(result.Credentials, func(i, j int) bool {
		if result.Credentials[i].Totals.Requests != result.Credentials[j].Totals.Requests {
			return result.Credentials[i].Totals.Requests > result.Credentials[j].Totals.Requests
		}
		return result.Credentials[i].CredentialID < result.Credentials[j].CredentialID
	})
	c.Header("Cache-Control", "no-store")
	response.SuccessI18n(c, "common.success", result)
}

type usageOverviewReader interface {
	AccessKeyUsageOverview(context.Context, requestlog.AccessKeyOverviewFilter) (requestlog.AccessKeyOverview, error)
}

func parseOverviewMS(raw string) int64 {
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 0 {
		return 0
	}
	return value
}

func parseOverviewID(raw string) uint {
	value, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || value == 0 {
		return 0
	}
	return uint(value)
}

func sortPeople(people []usageOverviewPerson) {
	sort.Slice(people, func(i, j int) bool {
		if people[i].Totals.Requests != people[j].Totals.Requests {
			return people[i].Totals.Requests > people[j].Totals.Requests
		}
		return people[i].AccessKeyID < people[j].AccessKeyID
	})
}

func (service *Service) accessKeyNames(c *gin.Context) (map[uint]string, error) {
	rows := make([]struct {
		ID   uint
		Name string
	}, 0, 16)
	if err := service.db.WithContext(c.Request.Context()).
		Table("access_keys").
		Select("id, name").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	names := make(map[uint]string, len(rows))
	for _, row := range rows {
		names[row.ID] = row.Name
	}
	return names, nil
}
