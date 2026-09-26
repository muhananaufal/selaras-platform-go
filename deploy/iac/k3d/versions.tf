# OpenTofu, not Terraform: OpenTofu is MPL-2.0 (LICENSE read at v1.12.6),
# Terraform is BSL since 1.6 (LICENSE read at v1.16.4). The HCL here is
# plain enough for both, but only OpenTofu is tested (CI job `infrastructure
# as code`), so only OpenTofu is claimed.
terraform {
  # Provider-defined functions (manifest_decode_multi) need 1.8 or later.
  required_version = ">= 1.8.0"

  required_providers {
    kubernetes = {
      source  = "hashicorp/kubernetes"
      version = "3.2.1"
    }
    helm = {
      source  = "hashicorp/helm"
      version = "3.3.0"
    }
  }
}
