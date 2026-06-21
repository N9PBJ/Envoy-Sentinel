# Project: Enphase DR Event Listener

## Goal

Create a Go application that monitors my Enphase Energy system and notifies me when a Demand Response (DR) event starts and ends.

My utility is GVEC in Texas. I participate in their battery program, which uses my Enphase IQ Batteries for demand response events.

Current problem:

* Neither GVEC nor Enphase provides notifications when a DR event starts or ends.
* I currently have to manually check the Enphase app throughout the day.
* I want automated notifications.

## Existing System

Hardware:

* Enphase solar system
* 6x Enphase IQ 5P batteries (~30 kWh total)
* Enphase gateway/controller
* Grid-connected
* Participating in GVEC demand response program

Behavior observed in real life:

* DR events occur frequently during summer.
* Batteries may discharge from near 100% down to the configured reserve level.
* During a DR event, batteries are generally prevented from recharging above reserve.
* Solar may continue producing, but excess energy may be exported rather than charging batteries while the event remains active.
* Enphase web/app UI displays a banner or profile indicating "DR Event Active".

Important:

* I do NOT know whether the API exposes DR event status directly.
* If no explicit DR flag exists, we may need to infer DR events from telemetry.

## Desired Features

### Phase 1 (MVP)

* Authenticate against Enphase API using OAuth2.
* Refresh tokens automatically.
* Poll API every few minutes.
* Determine whether a DR event is active.
* Detect transitions:

  * DR inactive -> active
  * DR active -> inactive
* Send notifications.

Notification examples:

DR Event Started
Time: 2026-06-18 15:42

DR Event Ended
Time: 2026-06-18 20:17
Duration: 4h 35m

### Phase 2

Collect historical data:

* Event start time
* Event end time
* Event duration
* Battery SOC at start/end
* Energy discharged during event

Store locally (SQLite is acceptable).

### Phase 3

Generate statistics:

* Number of DR events per month
* Total discharged kWh
* Average duration
* Frequency by month
* Other battery-program analytics

## Technical Preferences

Language:

* Go

Architecture:

* Single binary
* Minimal dependencies
* Clean package structure
* SQLite preferred over large database systems
* Runs continuously on Linux

Potential packages:

* net/http
* golang.org/x/oauth2
* modernc.org/sqlite or mattn/go-sqlite3

## Detection Strategy

First preference:

Determine whether the Enphase API already exposes DR event status directly.

Examples:

* dr_event_active
* demand_response_event
* grid_services_event
* profile status
* event state
* any equivalent field

Before implementing inference logic, inspect all available API responses and identify every field related to:

* battery status
* battery state of charge
* grid import/export
* production
* consumption
* system profile/state
* events
* grid services

If an explicit DR status exists, use it.

## Fallback Detection

If the API does NOT expose DR status directly:

Implement inferred detection.

Possible indicators:

* Battery rapidly discharges.
* Battery reaches reserve SOC.
* Battery remains pinned near reserve.
* Export activity occurs.
* Recharge behavior changes.
* Patterns match known DR behavior observed in the Enphase UI.

The exact algorithm should be developed only after examining actual API responses.

## Current Task

Review the Enphase Monitoring API documentation and identify:

1. OAuth flow required.
2. Required scopes/access controls.
3. Endpoints relevant to:

   * battery monitoring
   * live status
   * production
   * consumption
   * events
   * grid services
4. Whether DR event state is directly exposed.
5. Recommended API polling strategy.

Do not start coding yet.

First produce a technical design and identify the most useful endpoints for this project.
