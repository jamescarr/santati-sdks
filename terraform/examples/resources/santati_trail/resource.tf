resource "santati_trail" "billing" {
  name = "billing"
  # region = "us-east" # omit for the deployment's default region
}

output "billing_ingest_url" {
  value = santati_trail.billing.ingest_url
}
