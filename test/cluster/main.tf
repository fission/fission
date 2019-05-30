terraform {
  backend "gcs" {
    bucket = "fission-terraform-state"
    prefix = "fission-ci-cluster"
  }
}

provider "google" {
  version = "~> 2.7"
  project = "fission-ci"
}

resource "google_container_cluster" "fission-ci-1" {
  name                     = "fission-ci-1"
  description              = ""
  min_master_version       = "1.13.6-gke.0"
  network                  = "projects/fission-ci/global/networks/default"
  location                 = "us-central1-a"
  remove_default_node_pool = true
  initial_node_count       = 0
  logging_service          = "none"
  monitoring_service       = "none"
  master_authorized_networks_config {}
}

resource "google_container_node_pool" "pool-v1" {
  name     = "pool-v1"
  location = "us-central1-a"
  cluster  = google_container_cluster.fission-ci-1.name

  node_config {
    machine_type = "n1-standard-2"
    preemptible  = false
    disk_size_gb = 100
    oauth_scopes = [
      "https://www.googleapis.com/auth/devstorage.read_only",
      "https://www.googleapis.com/auth/monitoring.write",
    ]
  }

  initial_node_count = 3
  autoscaling {
    min_node_count = 3
    max_node_count = 5
  }

  management {
    auto_repair  = true
    auto_upgrade = true
  }
}
