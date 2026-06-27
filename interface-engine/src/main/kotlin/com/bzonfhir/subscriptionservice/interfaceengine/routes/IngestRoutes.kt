package com.bzonfhir.subscriptionservice.interfaceengine.routes

import com.bzonfhir.subscriptionservice.plugins.hl7v2mllp.Hl7V2MllpIngestSource

/**
 * Legacy facade for the receive path's route id (ticket #431, Epic #425).
 *
 * Before #431 this file contained the inline Camel-MLLP `RouteBuilder`
 * that owned the entire receive cycle (parse → persist → ACK). That code
 * has been refactored into the `plugins-builtin/hl7v2-mllp` module per
 * Epic #425's plugin-host refactor.
 *
 * The route itself is now built by `Hl7V2MllpCamelRouteBuilder` (inside
 * the plugin); the host wires the plugin into the persist pipeline via
 * [IngestSourceRegistry].
 *
 * What's left here: a constants-only facade exposing the canonical
 * route id so existing tests (`IngestRoutesTest.setUp()`) can reach
 * Camel's route registry by the same string they always have. A future
 * cleanup story may inline the constant at every callsite and delete
 * this file entirely; for #431's "keep existing tests green" scope we
 * keep the facade and re-export.
 *
 * NOTE: the constants below are aliases, not independent values. If you
 * change `Hl7V2MllpIngestSource.ROUTE_ID` you change this constant in
 * lockstep — there isn't a separate source of truth.
 */
object IngestRoutes {

    /**
     * Camel route id assigned to the MLLP listener route. Alias for
     * [Hl7V2MllpIngestSource.ROUTE_ID]. Tests that look up the
     * receive route by id (e.g. `camelContext.getRoute(IngestRoutes.ROUTE_MLLP_INGEST)`)
     * continue to work unchanged.
     */
    const val ROUTE_MLLP_INGEST: String = Hl7V2MllpIngestSource.ROUTE_ID

    // Camel header names — kept here as a courtesy to any downstream
    // code that imported them historically. The plugin's route builder
    // uses the same strings; these are aliases, not separate values.
    const val HDR_MESSAGE_TYPE: String = "hl7.messageType"
    const val HDR_CONTROL_ID: String = "hl7.controlId"
    const val HDR_SENDING_APP: String = "hl7.sendingApp"

    // Content-type stored on persisted rows for HL7 v2 wire payloads.
    // Aliases the plugin's [Hl7V2MessageParser.CONTENT_TYPE] value so a
    // single change at the SPI boundary propagates everywhere.
    const val RAW_CONTENT_TYPE_HL7V2: String = "application/hl7-v2"
}
