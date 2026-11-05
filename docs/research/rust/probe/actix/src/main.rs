use actix_web::{web, App, HttpServer};
use serde::Deserialize;
#[derive(Deserialize)] struct T { name: String, age: u32 }
#[derive(Deserialize)] struct Q { page: u32 }
async fn one(id: web::Path<u32>) -> String { format!("{id}") }
async fn two(p: web::Path<(u32,u32)>) -> String { format!("{:?}", p.into_inner()) }
async fn j(t: web::Json<T>) -> String { format!("{} {}", t.name, t.age) }
async fn q(q: web::Query<Q>) -> String { format!("{}", q.page) }
#[actix_web::main] async fn main() -> std::io::Result<()> {
  HttpServer::new(|| App::new().route("/u/{id}", web::get().to(one)).route("/m/{id}", web::get().to(two)).route("/j", web::post().to(j)).route("/q", web::get().to(q)))
   .bind("127.0.0.1:38082")?.workers(2).run().await
}
