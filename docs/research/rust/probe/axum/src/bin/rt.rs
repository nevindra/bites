use axum::{Router, routing::{get,post}, extract::{Path,Query}, Json};
use serde::Deserialize;
#[derive(Deserialize)] struct T { name: String, age: u32 }
#[derive(Deserialize)] struct Q { page: u32 }
async fn one(Path(id): Path<u32>) -> String { format!("{id}") }
async fn j(Json(t): Json<T>) -> String { format!("{} {}", t.name, t.age) }
async fn q(Query(q): Query<Q>) -> String { format!("{}", q.page) }
async fn two(Path((a,b)): Path<(u32,u32)>) -> String { format!("{a} {b}") }
#[tokio::main] async fn main(){
  let app = Router::new().route("/u/{id}", get(one)).route("/j", post(j)).route("/q", get(q)).route("/m/{id}", get(two));
  let l = tokio::net::TcpListener::bind("127.0.0.1:38081").await.unwrap();
  axum::serve(l, app).await.unwrap();
}
