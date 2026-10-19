import random

from locust import FastHttpUser, HttpUser, TaskSet, task

# The server seeds products 1 to 100 at startup.
SEEDED_PRODUCTS = 100


class ShopperTasks(TaskSet):
    """One request mix, shared by both user classes so the comparison is fair.

    Weights add up to 100, so each one is a percentage:
      80  GET an existing product        expect 200
      15  POST new details for it        expect 204
       5  GET a product that isn't there expect 404
    """

    @task(80)
    def view_product(self):
        product_id = random.randint(1, SEEDED_PRODUCTS)
        self.client.get(f"/products/{product_id}", name="GET /products/[id]")

    @task(15)
    def update_product(self):
        product_id = random.randint(1, SEEDED_PRODUCTS)
        self.client.post(
            f"/products/{product_id}/details",
            json={
                "product_id": product_id,
                "sku": f"SKU-{product_id:06d}",
                "manufacturer": "Locust",
                "category_id": random.randint(1, 500),
                "weight": random.randint(0, 5000),
                "some_other_id": random.randint(1, 1000),
            },
            name="POST /products/[id]/details",
        )

    @task(5)
    def view_missing_product(self):
        product_id = random.randint(10_000, 99_999)
        # A 404 is the correct answer here, so don't count it as a failure.
        with self.client.get(
            f"/products/{product_id}",
            name="GET /products/[missing-id]",
            catch_response=True,
        ) as response:
            if response.status_code == 404:
                response.success()
            else:
                response.failure(f"expected 404, got {response.status_code}")


class ProductHttpUser(HttpUser):
    """Sends requests with python-requests."""
    tasks = [ShopperTasks]


class ProductFastHttpUser(FastHttpUser):
    """Sends requests with geventhttpclient."""
    tasks = [ShopperTasks]