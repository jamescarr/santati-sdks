variable "siem_api_key" {
  type      = string
  sensitive = true
}

resource "santati_trail" "billing" {
  name = "billing"
}

resource "santati_log_stream" "siem" {
  name  = "siem-webhook"
  trail = santati_trail.billing.name

  config = {
    url             = "https://siem.example.com/santati"
    content_type    = "ndjson"
    timeout_seconds = 30
    headers = {
      "X-Source" = "santati"
    }
  }

  # Sent as the header "X-Api-Key: <value>" with every request.
  auth = {
    header_name  = "X-Api-Key"
    header_value = var.siem_api_key
  }

  # Forward only invoice events for us-east. Omit the block to forward every event.
  match_rules = {
    actions          = ["invoice.*"]
    actor_types      = ["user"]
    organization_ids = ["org_acme"]
    metadata = [
      { key = "region", op = "eq", value = "us-east" },
    ]
  }
}
