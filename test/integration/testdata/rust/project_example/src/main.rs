use axum::{Json, Router, body::Bytes, response::IntoResponse};
use serde_json::{Value, json};

async fn echo(body: Bytes) -> impl IntoResponse {
    // Echo JSON bodies as JSON, anything else as a string.
    let value: Value = serde_json::from_slice(&body)
        .unwrap_or_else(|_| Value::String(String::from_utf8_lossy(&body).into_owned()));
    Json(json!({ "echo": value }))
}

#[tokio::main]
async fn main() {
    let port: u16 = std::env::var("FISSION_RUNTIME_PORT")
        .ok()
        .and_then(|p| p.parse().ok())
        .unwrap_or(8889);
    let listener = tokio::net::TcpListener::bind(("127.0.0.1", port))
        .await
        .expect("bind function port");
    axum::serve(listener, Router::new().fallback(echo))
        .await
        .expect("server error");
}
