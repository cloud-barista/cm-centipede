// Matrix source seed - MongoDB
//
// 4 collections, 2 indexes, a validator and a view - the closest MongoDB has to
// the structure the SQL seeds carry. The multibyte documents are the charset
// check: Korean, Japanese and an emoji.
//
// COLLATION and withCollation() are defined by the prelude that
// scripts/init-mongodb.sh writes in front of this file, from
// MONGODB_SRC_COLLATION_LOCALE. Keeping the prelude out of here is what lets
// this file stay literal: nothing in it is substituted before mongosh sees it.
//
// Run against the database the init script selected, so no db name appears here.

db.createCollection("customers", withCollation({
  validator: {
    $jsonSchema: {
      bsonType: "object",
      required: ["name", "email"],
      properties: {
        name:  { bsonType: "string" },
        email: { bsonType: "string" }
      }
    }
  }
}));
db.createCollection("products",    withCollation({}));
db.createCollection("orders",      withCollation({}));
db.createCollection("order_items", withCollation({}));

db.customers.insertMany([
  { _id: 1, name: "Alice Kim",    email: "alice@example.com",   grade: "gold" },
  { _id: 2, name: "Bob Lee",      email: "bob@example.com",     grade: "silver" },
  { _id: 3, name: "Charlie Park", email: "charlie@example.com", grade: "bronze" },
  { _id: 4, name: "김철수 (한글)",  email: "utf8-ko@example.com", grade: "gold" },
  { _id: 5, name: "佐藤 太郎 🎌",   email: "utf8-ja@example.com", grade: "silver" }
]);

db.products.insertMany([
  { _id: 1, name: "Laptop Pro",     price: 1299.99, stock: 50 },
  { _id: 2, name: "Wireless Mouse", price: 29.99,   stock: 200 },
  { _id: 3, name: "USB-C Hub",      price: 49.99,   stock: 150 },
  { _id: 4, name: "4K Monitor",     price: 399.99,  stock: 30 },
  { _id: 5, name: "노트북 거치대 🖥",  price: 39.99,   stock: 120 }
]);

db.orders.insertMany([
  { _id: 1, customer_id: 1, status: "confirmed" },
  { _id: 2, customer_id: 2, status: "shipped" },
  { _id: 3, customer_id: 3, status: "pending" },
  { _id: 4, customer_id: 4, status: "confirmed" },
  { _id: 5, customer_id: 5, status: "shipped" }
]);

db.order_items.insertMany([
  { _id: 1, order_id: 1, product_id: 1, qty: 1, unit_price: 1299.99 },
  { _id: 2, order_id: 1, product_id: 2, qty: 2, unit_price: 29.99 },
  { _id: 3, order_id: 2, product_id: 3, qty: 1, unit_price: 49.99 },
  { _id: 4, order_id: 3, product_id: 4, qty: 1, unit_price: 399.99 },
  { _id: 5, order_id: 4, product_id: 5, qty: 2, unit_price: 39.99 },
  { _id: 6, order_id: 4, product_id: 2, qty: 1, unit_price: 29.99 },
  { _id: 7, order_id: 5, product_id: 1, qty: 1, unit_price: 1299.99 },
  { _id: 8, order_id: 5, product_id: 3, qty: 3, unit_price: 49.99 }
]);

db.orders.createIndex({ customer_id: 1 });
db.order_items.createIndex({ order_id: 1, product_id: 1 });

db.createView("v_order_summary", "orders", [
  { $group: { _id: "$customer_id", order_count: { $sum: 1 } } }
]);
