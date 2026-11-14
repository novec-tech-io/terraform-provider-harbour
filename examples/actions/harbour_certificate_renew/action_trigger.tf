# Renew whenever a value you control changes. Bump var.renew_epoch (for
# example to a date) and run a normal `terraform apply`.
#
# If the triggered renewal fails (for instance the 15-minute cooldown after a
# renewal), the apply exits non-zero but the trigger value is already
# recorded, so re-running `terraform apply` will NOT retry it. Retry with
# `terraform apply -invoke=action.harbour_certificate_renew.api` once the
# cause has cleared, or change the value again.
variable "renew_epoch" {
  type    = string
  default = "1"
}

resource "terraform_data" "renew_api" {
  input = var.renew_epoch

  lifecycle {
    action_trigger {
      events  = [after_update]
      actions = [action.harbour_certificate_renew.api]
    }
  }
}
