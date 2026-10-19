terraform {
  required_version = ">= 1.5.7"
  required_providers {
    google = { source = "hashicorp/google", version = "~> 7.0" }
  }
}
provider "google" {
  project = var.project_id
  region  = var.region
}
variable "project_id" { type = string }
variable "region" {
  type    = string
  default = "us-central1"
}
variable "image" {
  type        = string
  description = "Immutable Artifact Registry image digest; build and push separately."
  validation {
    condition     = can(regex("@sha256:[a-f0-9]{64}$", var.image))
    error_message = "Pin the release to a sha256 image digest."
  }
}
variable "origin" {
  type        = string
  description = "HTTPS API/custom-domain origin, without a trailing slash."
  validation {
    condition     = can(regex("^https://[^/]+$", var.origin))
    error_message = "An HTTPS origin without a path is required."
  }
}
variable "oidc_issuer" { type = string }
variable "oidc_client_id" { type = string }
variable "oidc_client_secret" { type = string }
variable "app_database_secret" {
  type        = string
  description = "Existing secret ID holding a non-owner PostgreSQL DSN using host=/cloudsql/CONNECTION_NAME."
}
variable "erp_database_secret" { type = string }
variable "migrator_database_secret" { type = string }
variable "erp_token_secret" { type = string }
variable "runtime_db_user" { type = string }
variable "erp_db_user" { type = string }
variable "sql_instance_connection_name" {
  type        = string
  description = "Existing Cloud SQL PostgreSQL 16 instance connection name. Backups/PITR and database users must be provisioned first."
}
locals {
  secret_roles = {
    api_db       = { actor = "api", secret = var.app_database_secret }
    worker_db    = { actor = "worker", secret = var.app_database_secret }
    erp_db       = { actor = "erp", secret = var.erp_database_secret }
    migration_db = { actor = "migrator", secret = var.migrator_database_secret }
    api_oidc     = { actor = "api", secret = var.oidc_client_secret }
    api_erp      = { actor = "api", secret = var.erp_token_secret }
    worker_erp   = { actor = "worker", secret = var.erp_token_secret }
    erp_token    = { actor = "erp", secret = var.erp_token_secret }
  }
}
resource "google_service_account" "service" {
  for_each   = toset(["api", "worker", "erp", "migrator", "scheduler"])
  account_id = "close-desk-${each.key}"
}
resource "google_project_iam_member" "sql" {
  for_each = toset(["api", "worker", "erp", "migrator"])
  project  = var.project_id
  role     = "roles/cloudsql.client"
  member   = "serviceAccount:${google_service_account.service[each.key].email}"
}
resource "google_secret_manager_secret_iam_member" "access" {
  for_each  = local.secret_roles
  project   = var.project_id
  secret_id = each.value.secret
  role      = "roles/secretmanager.secretAccessor"
  member    = "serviceAccount:${google_service_account.service[each.value.actor].email}"
}
resource "google_cloud_run_v2_service" "erp" {
  name                = "close-desk-erp"
  location            = var.region
  deletion_protection = true
  ingress             = "INGRESS_TRAFFIC_ALL"
  template {
    service_account = google_service_account.service["erp"].email
    scaling { max_instance_count = 2 }
    max_instance_request_concurrency = 16
    timeout                          = "10s"
    containers {
      image   = var.image
      command = ["/app/erpsim"]
      ports { container_port = 8091 }
      env {
        name  = "ERP_ADDR"
        value = "0.0.0.0:8091"
      }
      dynamic "env" {
        for_each = { DATABASE_URL = var.erp_database_secret, ERP_TOKEN = var.erp_token_secret }
        content {
          name = env.key
          value_source {
            secret_key_ref {
              secret  = env.value
              version = "latest"
            }
          }
        }
      }
      volume_mounts {
        name       = "cloudsql"
        mount_path = "/cloudsql"
      }
    }
    volumes {
      name = "cloudsql"
      cloud_sql_instance { instances = [var.sql_instance_connection_name] }
    }
  }
  depends_on = [google_project_iam_member.sql, google_secret_manager_secret_iam_member.access]
}
resource "google_cloud_run_v2_service" "api" {
  name                = "close-desk-api"
  location            = var.region
  deletion_protection = true
  ingress             = "INGRESS_TRAFFIC_ALL"
  template {
    service_account = google_service_account.service["api"].email
    scaling { max_instance_count = 3 }
    max_instance_request_concurrency = 24
    timeout                          = "50s"
    containers {
      image = var.image
      ports { container_port = 8090 }
      dynamic "env" {
        for_each = {
          APP_ORIGIN  = var.origin, LISTEN_ADDR = "0.0.0.0:8090", DEMO_PERSONAS = "false",
          OIDC_ISSUER = var.oidc_issuer, OIDC_CLIENT_ID = var.oidc_client_id,
          ERP_URL     = google_cloud_run_v2_service.erp.uri, ERP_IAM = "true"
        }
        content {
          name  = env.key
          value = env.value
        }
      }
      dynamic "env" {
        for_each = { DATABASE_URL = var.app_database_secret, OIDC_CLIENT_SECRET = var.oidc_client_secret, ERP_TOKEN = var.erp_token_secret }
        content {
          name = env.key
          value_source {
            secret_key_ref {
              secret  = env.value
              version = "latest"
            }
          }
        }
      }
      volume_mounts {
        name       = "cloudsql"
        mount_path = "/cloudsql"
      }
    }
    volumes {
      name = "cloudsql"
      cloud_sql_instance { instances = [var.sql_instance_connection_name] }
    }
  }
  depends_on = [google_project_iam_member.sql, google_secret_manager_secret_iam_member.access]
}
# Browser traffic is authenticated by the application's OIDC session, not Cloud Run IAM.
resource "google_cloud_run_v2_service_iam_member" "browser" {
  project  = var.project_id
  location = var.region
  name     = google_cloud_run_v2_service.api.name
  role     = "roles/run.invoker"
  member   = "allUsers"
}
resource "google_cloud_run_v2_service_iam_member" "erp" {
  for_each = toset(["api", "worker"])
  project  = var.project_id
  location = var.region
  name     = google_cloud_run_v2_service.erp.name
  role     = "roles/run.invoker"
  member   = "serviceAccount:${google_service_account.service[each.key].email}"
}
resource "google_cloud_run_v2_job" "migrator" {
  name                = "close-desk-migrator"
  location            = var.region
  deletion_protection = true
  template {
    template {
      service_account = google_service_account.service["migrator"].email
      timeout         = "120s"
      max_retries     = 0
      containers {
        image = var.image
        args  = ["-mode", "migrate"]
        dynamic "env" {
          for_each = { RUNTIME_DB_USER = var.runtime_db_user, ERP_DB_USER = var.erp_db_user }
          content {
            name  = env.key
            value = env.value
          }
        }
        env {
          name = "DATABASE_URL"
          value_source {
            secret_key_ref {
              secret  = var.migrator_database_secret
              version = "latest"
            }
          }
        }
        volume_mounts {
          name       = "cloudsql"
          mount_path = "/cloudsql"
        }
      }
      volumes {
        name = "cloudsql"
        cloud_sql_instance { instances = [var.sql_instance_connection_name] }
      }
    }
  }
  depends_on = [google_project_iam_member.sql, google_secret_manager_secret_iam_member.access]
}
resource "google_cloud_run_v2_job" "worker" {
  name                = "close-desk-worker"
  location            = var.region
  deletion_protection = true
  template {
    template {
      service_account = google_service_account.service["worker"].email
      timeout         = "120s"
      max_retries     = 1
      containers {
        image = var.image
        args  = ["-mode", "tick"]
        dynamic "env" {
          for_each = { ERP_URL = google_cloud_run_v2_service.erp.uri, ERP_IAM = "true" }
          content {
            name  = env.key
            value = env.value
          }
        }
        env {
          name = "DATABASE_URL"
          value_source {
            secret_key_ref {
              secret  = var.app_database_secret
              version = "latest"
            }
          }
        }
        dynamic "env" {
          for_each = { ERP_TOKEN = var.erp_token_secret }
          content {
            name = env.key
            value_source {
              secret_key_ref {
                secret  = env.value
                version = "latest"
              }
            }
          }
        }
        volume_mounts {
          name       = "cloudsql"
          mount_path = "/cloudsql"
        }
      }
      volumes {
        name = "cloudsql"
        cloud_sql_instance { instances = [var.sql_instance_connection_name] }
      }
    }
  }
  depends_on = [google_project_iam_member.sql, google_secret_manager_secret_iam_member.access]
}
resource "google_cloud_run_v2_job_iam_member" "scheduler" {
  project  = var.project_id
  location = var.region
  name     = google_cloud_run_v2_job.worker.name
  role     = "roles/run.invoker"
  member   = "serviceAccount:${google_service_account.service["scheduler"].email}"
}
resource "google_cloud_scheduler_job" "worker" {
  name      = "close-desk-delivery"
  schedule  = "* * * * *"
  region    = var.region
  time_zone = "Etc/UTC"
  paused    = true
  http_target {
    http_method = "POST"
    uri         = "https://run.googleapis.com/v2/projects/${var.project_id}/locations/${var.region}/jobs/${google_cloud_run_v2_job.worker.name}:run"
    body        = base64encode("{}")
    headers     = { "Content-Type" = "application/json" }
    oauth_token {
      service_account_email = google_service_account.service["scheduler"].email
    }
  }
  depends_on = [google_cloud_run_v2_job_iam_member.scheduler]
}
output "api_url" { value = google_cloud_run_v2_service.api.uri }
output "migration_job" { value = google_cloud_run_v2_job.migrator.name }
