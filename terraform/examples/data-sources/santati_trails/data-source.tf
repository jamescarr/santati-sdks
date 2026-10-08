data "santati_trails" "all" {}

output "trail_names" {
  value = [for trail in data.santati_trails.all.trails : trail.name]
}
