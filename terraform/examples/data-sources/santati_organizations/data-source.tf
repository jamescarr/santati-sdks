data "santati_organizations" "all" {}

output "organization_ids" {
  value = [for org in data.santati_organizations.all.organizations : org.external_id]
}
