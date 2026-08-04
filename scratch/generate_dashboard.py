import json

areas = [
    {"title": "APIs (HTTP/gRPC) RPM & Latency", "expr": "rate(http_server_duration_milliseconds_count[5m])"},
    {"title": "DB Commits", "expr": "rate(pg_stat_database_xact_commit[5m])"},
    {"title": "Idempotency Hits", "expr": "rate(idempotency_hits_total[5m])"},
    {"title": "Outbox Pending Events", "expr": "outbox_pending_events"},
    {"title": "RabbitMQ Queue Depth", "expr": "rabbitmq_queue_messages"},
    {"title": "Consolidation Gaps", "expr": "consolidation_gaps_total"},
    {"title": "Watermark Delay", "expr": "watermark_delay_seconds"},
    {"title": "Keycloak Active Sessions", "expr": "keycloak_active_sessions"}
]

panels = []
for i, area in enumerate(areas):
    panel = {
        "id": i + 1,
        "gridPos": {"h": 8, "w": 12, "x": (i % 2) * 12, "y": (i // 2) * 8},
        "type": "timeseries",
        "title": area["title"],
        "targets": [{"expr": area["expr"], "refId": "A"}],
        "datasource": {"type": "prometheus", "uid": "Prometheus"}
    }
    panels.append(panel)

dashboard = {
    "title": "Cashflow Observability",
    "uid": "cashflow-obs",
    "panels": panels,
    "schemaVersion": 36,
    "timezone": "browser"
}

with open("deploy/observability/grafana/dashboards/dashboard.json", "w") as f:
    json.dump(dashboard, f, indent=2)
