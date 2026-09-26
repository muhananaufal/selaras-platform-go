output "managed_manifests" {
  description = "Every object decoded from deploy/k8s, as kind/namespace/name."
  value       = sort(keys(local.manifests))
}
