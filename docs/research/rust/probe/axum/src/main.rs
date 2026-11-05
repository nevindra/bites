use axum::{Router, routing::get};
struct NotExtractor;
#[axum::debug_handler]
async fn bad(_x: NotExtractor) {}
#[tokio::main] async fn main(){ let _a: Router = Router::new().route("/", get(bad)); }
