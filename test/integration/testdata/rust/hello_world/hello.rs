// Single-file Fission Rust function: define `pub async fn handler`,
// using any axum handler signature (extractors, Json, ...).
use fission_rust::IntoResponse;

pub async fn handler() -> impl IntoResponse {
    "Hello, World!\n"
}
