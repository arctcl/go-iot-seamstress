# go-iot-seamstress

Archived Go + MQTT solution for piecework payroll control and end-to-end work-in-progress (WIP) tracking in a garment factory, from cut pieces to finished goods.

Zero-button hardware: Minimal action required from shop-floor workers.

Single SQLite DB: Lightweight, embedded, and easy to deploy.

User-friendly Web UI: Total control and beautiful stats for the production manager.

Core Workflow:
Order Creation: The Production Manager creates a manufacturing order in the web dashboard.
Cutting & Batching: The cutter issues a batch (a box with N number of pieces), prints a routing sheet (runner) with unique barcodes, and launches it onto the shop floor.
Operation Tracking: A seamstress scans her ID badge, scans the specific operation barcode, performs the task, and clicks "Ready" on her terminal (featuring a fact-check mechanism to prevent false entries).
Quality Control (QC/OTK): The QC inspector scans any barcode from the routing sheet and clicks either "Good" or "Not Good".If "Good": The batch is marked as finished.If "Not Good": The batch is rerouted back to the floor via the shop dispatcher for rework. (or create new box)
Real-Time Analytics Dashboard:
Performance Metrics: Real-time tracking of individual sewing speeds and efficiency for each seamstress.
Shop Floor Load: Live visualization of current factory capacity and workload utilization in percentages.
Production Planning: Dynamic data and statistics to optimize scheduling and detect processing bottlenecks instantly.
Leaderboards: Live "Top Seamstresses" charts to drive motivation and simplify piecework payroll calculations.