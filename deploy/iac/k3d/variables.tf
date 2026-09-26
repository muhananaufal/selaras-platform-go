variable "kubeconfig" {
  description = "Path to the kubeconfig that reaches the cluster."
  type        = string
  default     = "~/.kube/config"
}

variable "kube_context" {
  description = "kubeconfig context of the cluster; k3d names it k3d-<cluster>."
  type        = string
  default     = "k3d-selaras"
}
