use poem::{listener::TcpListener, Route, Server};
use poem_openapi::{param::{Path,Query}, payload::{Json,PlainText}, Object, OpenApi, OpenApiService};
#[derive(Object)] struct T { name: String, age: u32 }
struct Api;
#[OpenApi]
impl Api {
  #[oai(path="/u/:id", method="get")]
  async fn one(&self, id: Path<u32>) -> PlainText<String> { PlainText(format!("{}", id.0)) }
  #[oai(path="/q", method="get")]
  async fn q(&self, page: Query<u32>) -> PlainText<String> { PlainText(format!("{}", page.0)) }
  #[oai(path="/m/:nope", method="get")]
  async fn m(&self, id: Path<u32>) -> PlainText<String> { PlainText(format!("{}", id.0)) }
  #[oai(path="/j", method="post")]
  async fn j(&self, t: Json<T>) -> PlainText<String> { PlainText(format!("{} {}", t.0.name, t.0.age)) }
}
#[tokio::main] async fn main() -> Result<(), std::io::Error> {
  let svc = OpenApiService::new(Api, "t", "1.0");
  println!("SPEC {}", svc.spec());
  let app = Route::new().nest("/", svc);
  Server::new(TcpListener::bind("127.0.0.1:38083")).run(app).await
}
