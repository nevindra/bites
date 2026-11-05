use axum::{Router, routing::get, extract::Path, Json};
use serde::{Deserialize,Serialize};
use utoipa::{OpenApi, ToSchema};
#[derive(Serialize,ToSchema)] struct Pet { id: u64, name: String }
// Docs say u32 path param "id"; the code takes a string at a differently named route and returns 500s on mismatch.
#[utoipa::path(get, path="/pets/{id}", params(("id"=u64, Path)), responses((status=200, body=Pet)))]
async fn get_pet(Path(id): Path<String>) -> Json<Pet> { Json(Pet{id:0,name:id}) }
#[derive(OpenApi)] #[openapi(paths(get_pet))] struct Doc;

use aide::axum::{ApiRouter, routing::get as aget};
#[derive(Serialize, schemars::JsonSchema)] struct P2 { id: u64 }
async fn aget_pet(Path(id): Path<u64>) -> Json<P2> { Json(P2{id}) }
#[derive(Deserialize, schemars::JsonSchema)] struct PP { id: u64 }
async fn aget2(Path(p): Path<PP>) -> Json<P2> { Json(P2{id:p.id}) }
#[tokio::main] async fn main(){
  aide::generate::on_error(|e| println!("AIDE-ERR: {e}"));
  println!("UTOIPA: {}", Doc::openapi().to_pretty_json().unwrap());
  // route registered at a DIFFERENT path than the annotation: compiles fine
  let _r: Router = Router::new().route("/animals/{id}", get(get_pet));
  let mut api = aide::openapi::OpenApi::default();
  let _app: Router = ApiRouter::new().api_route("/p/{id}", aget(aget_pet)).api_route("/q/{id}", aget(aget2)).api_route("/r/{wrong}", aget(aget2)).finish_api(&mut api);
  println!("AIDE: {}", serde_json::to_string(&api.paths).unwrap());
}
