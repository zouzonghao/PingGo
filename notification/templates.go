package notification

import (
	"bytes"
	"html/template"
)

// StatusChangeData holds data for the status change email template
type StatusChangeData struct {
	Name       string
	URL        string
	OldStatus  string
	NewStatus  string
	Message    string
	Color      string
	StatusText string
	DateTime   string
}

// DailyReportData holds data for the daily report email template
type DailyReportData struct {
	Date          string
	TotalCount    int
	UptimePercent float64
	DownCount     int
	DownColor     string
	Monitors      []MonitorInfo
}

// MonitorInfo holds individual monitor stats for the report
type MonitorInfo struct {
	Name           string
	Type           string
	Uptime24h      float64
	AvgResponse24h int64
	Status         string
	Color          string
	UptimeColor    string
	RowBg          string
}

const statusChangeTemplate = `
<!DOCTYPE html>
<html>
<head>
	<meta charset="utf-8">
	<meta name="viewport" content="width=device-width, initial-scale=1.0">
</head>
<body style="margin: 0; padding: 0; background-color: #f6f9fc; font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, 'Helvetica Neue', Arial, sans-serif;">
	<div style="max-width: 600px; margin: 0 auto; background-color: #ffffff; border-radius: 12px; overflow: hidden; box-shadow: 0 4px 6px rgba(0,0,0,0.05); margin-top: 20px; margin-bottom: 20px;">
		<!-- Header -->
		<div style="background-color: {{.Color}}; padding: 30px 40px; text-align: center;">
			<h1 style="margin: 0; color: #ffffff; font-size: 24px; font-weight: 700; letter-spacing: 0.5px;">{{.StatusText}}</h1>
			<p style="margin: 10px 0 0; color: rgba(255,255,255,0.9); font-size: 14px;">{{.DateTime}}</p>
		</div>

		<!-- Status Card -->
		<div style="padding: 30px 40px; background-color: #ffffff;">
			<div style="text-align: center; margin-bottom: 30px; padding-bottom: 30px; border-bottom: 1px solid #f1f5f9;">
				<div style="font-size: 20px; font-weight: 700; color: #1e293b; margin-bottom: 5px;">{{.Name}}</div>
				<a href="{{.URL}}" style="font-size: 14px; color: #64748b; text-decoration: none; word-break: break-all;">{{.URL}}</a>
			</div>

			<div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 30px; background-color: #f8fafc; padding: 20px; border-radius: 8px;">
				<div style="text-align: center; flex: 1;">
					<div style="font-size: 12px; color: #94a3b8; text-transform: uppercase; font-weight: 600; margin-bottom: 5px;">Previous Status</div>
					<div style="font-size: 16px; font-weight: 700; color: #64748b;">{{.OldStatus}}</div>
				</div>
				<div style="color: #cbd5e1; font-size: 20px;">&rarr;</div>
				<div style="text-align: center; flex: 1;">
					<div style="font-size: 12px; color: #94a3b8; text-transform: uppercase; font-weight: 600; margin-bottom: 5px;">Current Status</div>
					<div style="font-size: 16px; font-weight: 700; color: {{.Color}};">{{.NewStatus}}</div>
				</div>
			</div>

			<!-- Details -->
			<div style="background-color: #fff; border: 1px solid #e2e8f0; border-radius: 8px; overflow: hidden;">
				<div style="padding: 12px 20px; background-color: #f8fafc; border-bottom: 1px solid #e2e8f0; font-size: 13px; font-weight: 600; color: #475569; text-transform: uppercase;">
					Message Detail
				</div>
				<div style="padding: 20px; color: #334155; font-size: 14px; line-height: 1.6; font-family: monospace; white-space: pre-wrap;">{{.Message}}</div>
			</div>
		</div>

		<!-- Footer -->
		<div style="padding: 20px 40px; background-color: #f1f5f9; text-align: center; border-bottom-left-radius: 12px; border-bottom-right-radius: 12px;">
			<p style="margin: 0; color: #94a3b8; font-size: 12px;">
				PingGo Monitor System
			</p>
		</div>
	</div>
</body>
</html>
`

const dailyReportTemplate = `
<!DOCTYPE html>
<html>
<head>
	<meta charset="utf-8">
	<meta name="viewport" content="width=device-width, initial-scale=1.0">
</head>
<body style="margin: 0; padding: 0; background-color: #f6f9fc; font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, 'Helvetica Neue', Arial, sans-serif;">
	<div style="max-width: 600px; margin: 0 auto; background-color: #ffffff; border-radius: 12px; overflow: hidden; box-shadow: 0 4px 6px rgba(0,0,0,0.05); margin-top: 20px; margin-bottom: 20px;">
		<!-- Header -->
		<div style="background-color: #2ecc71; padding: 30px 40px; text-align: center;">
			<h1 style="margin: 0; color: #ffffff; font-size: 24px; font-weight: 700; letter-spacing: 0.5px;">PingGo 每日速报</h1>
			<p style="margin: 10px 0 0; color: rgba(255,255,255,0.9); font-size: 14px;">{{.Date}}</p>
		</div>

		<!-- Summary Cards -->
		<div style="padding: 30px 40px; background-color: #f8f9fa; border-bottom: 1px solid #edf2f7;">
			<div style="display: grid; grid-template-columns: repeat(3, 1fr); gap: 15px; text-align: center;">
				<div style="background: white; padding: 15px; border-radius: 8px; border: 1px solid #e2e8f0;">
					<div style="font-size: 12px; color: #64748b; text-transform: uppercase; font-weight: 600;">监控总数</div>
					<div style="font-size: 24px; font-weight: 800; color: #1e293b; margin-top: 5px;">{{.TotalCount}}</div>
				</div>
				<div style="background: white; padding: 15px; border-radius: 8px; border: 1px solid #e2e8f0;">
					<div style="font-size: 12px; color: #64748b; text-transform: uppercase; font-weight: 600;">系统在线率</div>
					<div style="font-size: 24px; font-weight: 800; color: #2ecc71; margin-top: 5px;">{{printf "%.1f" .UptimePercent}}%</div>
				</div>
				<div style="background: white; padding: 15px; border-radius: 8px; border: 1px solid #e2e8f0;">
					<div style="font-size: 12px; color: #64748b; text-transform: uppercase; font-weight: 600;">异常服务</div>
					<div style="font-size: 24px; font-weight: 800; color: {{.DownColor}}; margin-top: 5px;">{{.DownCount}}</div>
				</div>
			</div>
		</div>

		<!-- Detail List -->
		<div style="padding: 30px 40px;">
			<h3 style="margin: 0 0 20px; color: #334155; font-size: 16px; font-weight: 700;">监控详情</h3>
			<table style="width: 100%; border-collapse: collapse;">
				<thead style="background-color: #f8f9fa; color: #64748b; font-size: 12px; text-transform: uppercase; text-align: left;">
					<tr>
						<th style="padding: 12px 15px; border-radius: 6px 0 0 6px;">服务名称</th>
						<th style="padding: 12px 15px; text-align: center;">24h 在线率</th>
						<th style="padding: 12px 15px; text-align: center;">平均延迟</th>
						<th style="padding: 12px 15px; text-align: right; border-radius: 0 6px 6px 0;">状态</th>
					</tr>
				</thead>
				<tbody style="font-size: 14px; color: #334155;">
					{{range .Monitors}}
					<tr style="background-color: {{.RowBg}};">
						<td style="padding: 12px 15px; border-bottom: 1px solid #f1f5f9;">
							<div style="font-weight: 600;">{{.Name}}</div>
							<div style="font-size: 11px; color: #94a3b8; margin-top: 2px;">{{.Type}}</div>
						</td>
						<td style="padding: 12px 15px; border-bottom: 1px solid #f1f5f9; text-align: center; font-family: monospace; font-weight: 600; color: {{.UptimeColor}};">
							{{printf "%.1f" .Uptime24h}}%
						</td>
						<td style="padding: 12px 15px; border-bottom: 1px solid #f1f5f9; text-align: center; font-family: monospace;">
							{{.AvgResponse24h}} ms
						</td>
						<td style="padding: 12px 15px; text-align: right; border-bottom: 1px solid #f1f5f9;">
							<span style="display: inline-block; padding: 4px 10px; border-radius: 20px; font-size: 12px; font-weight: 600; background-color: {{.Color}}15; color: {{.Color}};">
								{{.Status}}
							</span>
						</td>
					</tr>
					{{end}}
				</tbody>
			</table>
		</div>

		<!-- Footer -->
		<div style="padding: 20px 40px; background-color: #f1f5f9; text-align: center; border-bottom-left-radius: 12px; border-bottom-right-radius: 12px;">
			<p style="margin: 0; color: #94a3b8; font-size: 12px;">
				PingGo Monitor System &bull; <a href="#" style="color: #94a3b8; text-decoration: none;">Manage Notifications</a>
			</p>
		</div>
	</div>
</body>
</html>
`

// RenderStatusChangeEmail renders the status change HTML email
func RenderStatusChangeEmail(data StatusChangeData) (string, error) {
	tmpl, err := template.New("status_change").Parse(statusChangeTemplate)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// RenderDailyReportEmail renders the daily report HTML email
func RenderDailyReportEmail(data DailyReportData) (string, error) {
	tmpl, err := template.New("daily_report").Parse(dailyReportTemplate)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}
