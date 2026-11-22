// frontend/src/pages/QueuePage.tsx
import { useContext, useState, useMemo } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "react-router-dom";
import { getHITLQueue, clearEscalations } from "../lib/api";
import { QueueItem } from "../types";
import { ThemeContext } from "../App";

function timeLeft(deadline: number): { label: string; urgent: boolean } {
  const s = deadline - Date.now() / 1000;
  if (s <= 0)  return { label: "OVERDUE",              urgent: true  };
  if (s < 300) return { label: `${Math.round(s/60)}m`, urgent: true  };
  const m = Math.floor(s / 60);
  if (m < 60)  return { label: `${m}m left`,           urgent: false };
  return         { label: `${Math.floor(m/60)}h ${m%60}m left`, urgent: false };
}

function groupByDocument(items: any[]): Map<string, { docId: string; fields: any[] }> {
  const map = new Map<string, { docId: string; fields: any[] }>();
  for (const item of items) {
    const result = item.extraction_result ?? item;
    const docId  = item.id ?? result.id ?? "unknown";
    if (!map.has(docId)) map.set(docId, { docId, fields: [] });
    map.get(docId)!.fields.push(item);
  }
  return map;
}

export default function QueuePage() {
  const navigate    = useNavigate();
  const queryClient = useQueryClient();
  const { dark }    = useContext(ThemeContext);

  const [selectedDoc, setSelectedDoc] = useState<string | null>(null);
  const [panelOpen,   setPanelOpen]   = useState(true);

  const t = {
    bg:           dark ? "#0f172a" : "#f1f5f9",
    text:         dark ? "#e2e8f0" : "#1e293b",
    subtext:      dark ? "#475569" : "#64748b",
    dim:          dark ? "#334155" : "#94a3b8",
    card:         dark ? "#1e293b" : "#ffffff",
    cardBorder:   dark ? "#334155" : "#e2e8f0",
    cardHover:    dark ? "#243447" : "#f8fafc",
    statCard:     dark ? "#1e293b" : "#ffffff",
    statBorder:   dark ? "#334155" : "#e2e8f0",
    sectionTitle: dark ? "#475569" : "#94a3b8",
    itemVal:      dark ? "#e2e8f0" : "#1e293b",
    panel:        dark ? "#141e2e" : "#f8fafc",
    panelBorder:  dark ? "#1e2d40" : "#e2e8f0",
    panelHdr:     dark ? "#1e293b" : "#ffffff",
    fieldBorder:  dark ? "#1e2d40" : "#f1f5f9",
  };

  const queueQuery = useQuery<{ items: QueueItem[]; queue_depth: number; escalations: any[] }>({
    queryKey:        ["queue-full"],
    queryFn:         getHITLQueue,
    refetchInterval: 8000,
  });

  const clearEscalationsMutation = useMutation({
    mutationFn: clearEscalations,
    onSuccess:  () => queryClient.invalidateQueries({ queryKey: ["queue-full"] }),
  });

  const items       = queueQuery.data?.items       ?? [];
  const depth       = queueQuery.data?.queue_depth ?? 0;
  const escalations = queueQuery.data?.escalations ?? [];

  const docGroups  = useMemo(() => groupByDocument(items), [items]);
  const docEntries = useMemo(() => Array.from(docGroups.values()), [docGroups]);

  // Fields shown in main frame — either from selected doc or all items
  const visibleFields = useMemo(() => {
    if (!selectedDoc) return items;
    return docGroups.get(selectedDoc)?.fields ?? [];
  }, [selectedDoc, items, docGroups]);

  const PANEL_W = panelOpen ? 260 : 44;

  return (
    <div style={{
      display: "flex", fontFamily: "'Sora', sans-serif",
      color: t.text, minHeight: "calc(100vh - 54px)",
      background: t.bg, transition: "background .2s, color .2s",
    }}>

      {/* ═══════════════════════════════════════
          LEFT PANEL — Document list
      ═══════════════════════════════════════ */}
      <div style={{
        width: `${PANEL_W}px`, minWidth: `${PANEL_W}px`,
        background: t.panel,
        borderRight: `1px solid ${t.panelBorder}`,
        display: "flex", flexDirection: "column",
        transition: "width .2s ease, min-width .2s ease",
        overflow: "hidden",
        position: "sticky", top: 0,
        height: "calc(100vh - 54px)",
        flexShrink: 0,
      }}>

        {/* Panel header */}
        <div style={{
          display: "flex", alignItems: "center",
          justifyContent: panelOpen ? "space-between" : "center",
          padding: panelOpen ? "14px 14px 12px" : "14px 0",
          background: t.panelHdr,
          borderBottom: `1px solid ${t.panelBorder}`,
          flexShrink: 0,
        }}>
          {panelOpen && (
            <div style={{ display: "flex", alignItems: "center", gap: "8px" }}>
              <span style={{
                fontSize: "11px", fontWeight: 700, textTransform: "uppercase",
                letterSpacing: ".07em", color: t.sectionTitle,
              }}>
                Documents
              </span>
              <span style={{
                fontSize: "10px", fontWeight: 700,
                background: docEntries.length > 0 ? "rgba(59,130,246,.15)" : "rgba(34,197,94,.15)",
                color:      docEntries.length > 0 ? "#3b82f6" : "#22c55e",
                borderRadius: "10px", padding: "1px 7px",
                fontFamily: "'IBM Plex Mono', monospace",
              }}>
                {docEntries.length}
              </span>
            </div>
          )}
          <button
            onClick={() => setPanelOpen(p => !p)}
            title={panelOpen ? "Collapse" : "Expand"}
            style={{
              background: "none", border: "none", cursor: "pointer",
              color: t.subtext, padding: "2px 4px", borderRadius: "6px",
              fontSize: "16px", lineHeight: 1, display: "flex", alignItems: "center",
              transition: "color .15s",
            }}
            onMouseEnter={e => (e.currentTarget.style.color = t.text)}
            onMouseLeave={e => (e.currentTarget.style.color = t.subtext)}
          >
            {panelOpen ? "‹" : "›"}
          </button>
        </div>

        {/* ── Expanded: flat document list ── */}
        {panelOpen && (
          <div style={{ overflowY: "auto", flex: 1, padding: "8px 0" }}>

            {queueQuery.isLoading && (
              <div style={{ display: "flex", justifyContent: "center", padding: "32px" }}>
                <div style={{
                  width: "22px", height: "22px",
                  border: `2px solid ${t.panelBorder}`, borderTopColor: "#3b82f6",
                  borderRadius: "50%", animation: "spin .8s linear infinite",
                }} />
              </div>
            )}

            {!queueQuery.isLoading && docEntries.length === 0 && (
              <div style={{ padding: "24px 14px", textAlign: "center", color: t.subtext, fontSize: "12px" }}>
                No pending documents
              </div>
            )}

            {/* "All documents" row */}
            {docEntries.length > 0 && (
              <div
                onClick={() => setSelectedDoc(null)}
                style={{
                  display: "flex", alignItems: "center", gap: "10px",
                  padding: "9px 14px",
                  cursor: "pointer",
                  borderLeft: `3px solid ${selectedDoc === null ? "#3b82f6" : "transparent"}`,
                  background: selectedDoc === null
                    ? (dark ? "rgba(59,130,246,.1)" : "rgba(59,130,246,.06)")
                    : "transparent",
                  transition: "all .12s",
                }}
                onMouseEnter={e => { if (selectedDoc !== null) e.currentTarget.style.background = dark ? "rgba(255,255,255,.04)" : "rgba(0,0,0,.03)"; }}
                onMouseLeave={e => { if (selectedDoc !== null) e.currentTarget.style.background = "transparent"; }}
              >
                <span style={{ fontSize: "13px" }}>📋</span>
                <div style={{ flex: 1, minWidth: 0 }}>
                  <div style={{ fontSize: "12px", fontWeight: 600, color: t.text }}>All documents</div>
                  <div style={{ fontSize: "10px", color: t.subtext, marginTop: "1px" }}>
                    {items.length} field{items.length !== 1 ? "s" : ""} total
                  </div>
                </div>
                <span style={{
                  fontSize: "9px", fontWeight: 700,
                  color: "#3b82f6", background: "rgba(59,130,246,.12)",
                  borderRadius: "6px", padding: "2px 6px", flexShrink: 0,
                }}>
                  {items.length}
                </span>
              </div>
            )}

            {/* Divider */}
            {docEntries.length > 0 && (
              <div style={{ height: "1px", background: t.panelBorder, margin: "4px 14px 4px" }} />
            )}

            {/* One row per document */}
            {docEntries.map(({ docId, fields }) => {
              const isSelected = selectedDoc === docId;
              const hasUrgent  = fields.some(f => timeLeft(f.deadline).urgent);
              const allConf    = fields.map(f => Math.round(((f.extraction_result ?? f).confidence_score ?? 0) * 100));
              const avgConf    = allConf.length ? Math.round(allConf.reduce((a, b) => a + b, 0) / allConf.length) : 0;

              return (
                <div
                  key={docId}
                  onClick={() => setSelectedDoc(docId)}
                  style={{
                    display: "flex", alignItems: "center", gap: "10px",
                    padding: "9px 14px",
                    cursor: "pointer",
                    borderLeft: `3px solid ${hasUrgent ? "#ef4444" : isSelected ? "#3b82f6" : "transparent"}`,
                    background: isSelected
                      ? (dark ? "rgba(59,130,246,.1)" : "rgba(59,130,246,.06)")
                      : "transparent",
                    transition: "all .12s",
                  }}
                  onMouseEnter={e => { if (!isSelected) e.currentTarget.style.background = dark ? "rgba(255,255,255,.04)" : "rgba(0,0,0,.03)"; }}
                  onMouseLeave={e => { if (!isSelected) e.currentTarget.style.background = "transparent"; }}
                >
                  {/* File icon */}
                  <span style={{ fontSize: "13px", flexShrink: 0 }}>
                    {hasUrgent ? "🔴" : "📄"}
                  </span>

                  {/* Doc info */}
                  <div style={{ flex: 1, minWidth: 0 }}>
                    <div style={{
                      fontSize: "11px", fontWeight: 600, color: t.text,
                      fontFamily: "'IBM Plex Mono', monospace",
                      overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap",
                    }}>
                      {String(docId).slice(0, 10)}…
                    </div>
                    <div style={{ display: "flex", alignItems: "center", gap: "6px", marginTop: "2px" }}>
                      <span style={{ fontSize: "10px", color: t.subtext }}>
                        {fields.length} pending
                      </span>
                      <span style={{
                        fontSize: "9px", fontWeight: 600,
                        color: avgConf >= 85 ? "#22c55e" : avgConf >= 50 ? "#f59e0b" : "#ef4444",
                        fontFamily: "'IBM Plex Mono', monospace",
                      }}>
                        ~{avgConf}%
                      </span>
                    </div>
                  </div>

                  {/* Urgent badge or count */}
                  {hasUrgent ? (
                    <span style={{
                      fontSize: "9px", fontWeight: 700, color: "#ef4444",
                      background: "rgba(239,68,68,.12)", borderRadius: "6px",
                      padding: "2px 6px", flexShrink: 0,
                      animation: "blink 1.5s infinite",
                    }}>!</span>
                  ) : (
                    <span style={{
                      fontSize: "9px", fontWeight: 700,
                      color: "#3b82f6", background: "rgba(59,130,246,.12)",
                      borderRadius: "6px", padding: "2px 6px", flexShrink: 0,
                    }}>{fields.length}</span>
                  )}
                </div>
              );
            })}
          </div>
        )}

        {/* ── Collapsed: mini doc chips ── */}
        {!panelOpen && (
          <div style={{
            overflowY: "auto", flex: 1, padding: "8px 0",
            display: "flex", flexDirection: "column", alignItems: "center", gap: "6px",
          }}>
            {docEntries.map(({ docId, fields }) => {
              const hasUrgent  = fields.some(f => timeLeft(f.deadline).urgent);
              const isSelected = selectedDoc === docId;
              return (
                <div
                  key={docId}
                  title={`Doc ${String(docId).slice(0, 8)} — ${fields.length} pending`}
                  onClick={() => { setPanelOpen(true); setSelectedDoc(docId); }}
                  style={{
                    width: "28px", height: "28px", borderRadius: "6px",
                    background: hasUrgent
                      ? "rgba(239,68,68,.15)"
                      : isSelected
                        ? "rgba(59,130,246,.2)"
                        : "rgba(59,130,246,.1)",
                    border: `1px solid ${hasUrgent ? "rgba(239,68,68,.4)" : "rgba(59,130,246,.3)"}`,
                    display: "flex", alignItems: "center", justifyContent: "center",
                    cursor: "pointer", fontSize: "10px", fontWeight: 700,
                    color: hasUrgent ? "#ef4444" : "#3b82f6",
                    fontFamily: "'IBM Plex Mono', monospace",
                    transition: "all .12s",
                  }}
                  onMouseEnter={e => { e.currentTarget.style.transform = "scale(1.1)"; }}
                  onMouseLeave={e => { e.currentTarget.style.transform = "scale(1)"; }}
                >
                  {fields.length}
                </div>
              );
            })}
          </div>
        )}
      </div>

      {/* ═══════════════════════════════════════
          MAIN FRAME
      ═══════════════════════════════════════ */}
      <div style={{ flex: 1, padding: "32px", overflowY: "auto", minWidth: 0 }}>

        {/* Header */}
        <div style={{
          display: "flex", alignItems: "flex-start", justifyContent: "space-between",
          marginBottom: "32px", gap: "20px", flexWrap: "wrap",
        }}>
          <div>
            <h1 style={{
              fontSize: "26px", fontWeight: 700, margin: "0 0 4px",
              background: "linear-gradient(135deg, #60a5fa, #a78bfa)",
              WebkitBackgroundClip: "text", WebkitTextFillColor: "transparent",
            }}>
              {selectedDoc ? "Pending Fields" : "Review Queue"}
            </h1>
            <p style={{ fontSize: "13px", color: t.subtext, margin: 0 }}>
              {selectedDoc
                ? <>Doc <span style={{ fontFamily: "'IBM Plex Mono', monospace" }}>{String(selectedDoc).slice(0, 12)}…</span> — {visibleFields.length} field{visibleFields.length !== 1 ? "s" : ""} pending</>
                : "Fields awaiting human verification, ordered by priority"
              }
            </p>
          </div>

          {/* Stats + action */}
          <div style={{ display: "flex", alignItems: "center", gap: "12px", flexWrap: "wrap" }}>
            <div style={{
              display: "flex", flexDirection: "column", alignItems: "center",
              background: t.statCard, border: `1px solid ${t.statBorder}`,
              borderRadius: "10px", padding: "10px 18px", minWidth: "70px",
            }}>
              <span style={{
                fontSize: "24px", fontWeight: 700,
                fontFamily: "'IBM Plex Mono', monospace",
                color: depth > 20 ? "#ef4444" : depth > 5 ? "#f59e0b" : "#22c55e",
              }}>{depth}</span>
              <span style={{ fontSize: "10px", color: t.subtext, textTransform: "uppercase", letterSpacing: ".06em" }}>pending</span>
            </div>

            <div style={{
              display: "flex", flexDirection: "column", alignItems: "center",
              background: t.statCard, border: `1px solid ${t.statBorder}`,
              borderRadius: "10px", padding: "10px 18px", minWidth: "70px",
            }}>
              <span style={{
                fontSize: "24px", fontWeight: 700,
                fontFamily: "'IBM Plex Mono', monospace",
                color: escalations.length > 0 ? "#ef4444" : "#22c55e",
              }}>{escalations.length}</span>
              <span style={{ fontSize: "10px", color: t.subtext, textTransform: "uppercase", letterSpacing: ".06em" }}>escalations</span>
            </div>

          </div>
        </div>

        {/* ── Body grid ── */}
        <div style={{
          display: "grid",
          gridTemplateColumns: escalations.length > 0 && !selectedDoc ? "1fr 1fr" : "1fr",
          gap: "24px",
        }}>

          {/* Pending fields (filtered by selected doc or all) */}
          <div style={{ display: "flex", flexDirection: "column", gap: "12px" }}>
            <h2 style={{
              fontSize: "12px", fontWeight: 700, textTransform: "uppercase",
              letterSpacing: ".07em", color: t.sectionTitle, margin: 0,
            }}>
              {selectedDoc ? "Fields in this document" : "Pending items"}
            </h2>

            {queueQuery.isLoading && (
              <div style={{ display: "flex", justifyContent: "center", padding: "40px" }}>
                <div style={{
                  width: "32px", height: "32px",
                  border: `3px solid ${t.cardBorder}`, borderTopColor: "#3b82f6",
                  borderRadius: "50%", animation: "spin .8s linear infinite",
                }} />
              </div>
            )}

            {!queueQuery.isLoading && visibleFields.length === 0 && (
              <div style={{
                display: "flex", flexDirection: "column", alignItems: "center",
                gap: "8px", padding: "48px", color: "#22c55e", textAlign: "center",
              }}>
                <span style={{ fontSize: "32px" }}>✓</span>
                <p style={{ fontSize: "13px", margin: 0, color: t.subtext }}>
                  {selectedDoc ? "No pending fields for this document" : "Queue is clear — no items pending review"}
                </p>
              </div>
            )}

            <div style={{ display: "flex", flexDirection: "column", gap: "8px" }}>
              {visibleFields.map((item: any) => {
                const result       = item.extraction_result ?? item;
                const extractionId = item.id ?? result.id;
                const { label, urgent } = timeLeft(item.deadline);
                const conf = Math.round((result.confidence_score ?? 0) * 100);

                return (
                  <div
                    key={item.item_id}
                    onClick={() => extractionId && navigate(`/review/${extractionId}`)}
                    style={{
                      position: "relative", overflow: "hidden",
                      background: urgent
                        ? dark ? "rgba(239,68,68,.05)" : "rgba(239,68,68,.03)"
                        : t.card,
                      border: `1px solid ${urgent ? "rgba(239,68,68,.4)" : t.cardBorder}`,
                      borderRadius: "10px", padding: "12px 14px",
                      cursor: "pointer", transition: "all .15s",
                      display: "flex", flexDirection: "column", gap: "6px",
                    }}
                    onMouseEnter={e => {
                      e.currentTarget.style.background   = urgent ? (dark ? "rgba(239,68,68,.08)" : "rgba(239,68,68,.05)") : t.cardHover;
                      e.currentTarget.style.borderColor  = urgent ? "rgba(239,68,68,.6)" : "#3b82f6";
                      e.currentTarget.style.transform    = "translateX(2px)";
                    }}
                    onMouseLeave={e => {
                      e.currentTarget.style.background  = urgent ? (dark ? "rgba(239,68,68,.05)" : "rgba(239,68,68,.03)") : t.card;
                      e.currentTarget.style.borderColor = urgent ? "rgba(239,68,68,.4)" : t.cardBorder;
                      e.currentTarget.style.transform   = "translateX(0)";
                    }}
                  >
                    {urgent && (
                      <div style={{ position: "absolute", left: 0, top: 0, bottom: 0, width: "3px", background: "#ef4444" }} />
                    )}

                    <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", gap: "8px" }}>
                      <span style={{ fontSize: "11px", fontWeight: 700, textTransform: "uppercase", letterSpacing: ".05em", color: t.subtext }}>
                        {result.field_name ?? "—"}
                      </span>
                      <span style={{
                        fontSize: "11px",
                        color: urgent ? "#ef4444" : t.subtext,
                        fontWeight: urgent ? 700 : 400,
                        animation: urgent ? "blink 1.5s infinite" : "none",
                      }}>
                        {label}
                      </span>
                    </div>

                    <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", gap: "8px" }}>
                      <span style={{
                        fontSize: "13px", color: t.itemVal,
                        whiteSpace: "nowrap", overflow: "hidden", textOverflow: "ellipsis", maxWidth: "200px",
                      }}>
                        {result.value ?? <em style={{ color: t.subtext, fontStyle: "italic", fontSize: "12px" }}>no value</em>}
                      </span>
                      <span style={{
                        fontSize: "11px", fontWeight: 700,
                        fontFamily: "'IBM Plex Mono', monospace", flexShrink: 0,
                        color: conf >= 85 ? "#22c55e" : conf >= 50 ? "#f59e0b" : "#ef4444",
                      }}>
                        {conf}% conf
                      </span>
                    </div>

                    <div style={{ display: "flex", alignItems: "center", gap: "8px" }}>
                      <span style={{ fontSize: "10px", color: t.dim, fontFamily: "'IBM Plex Mono', monospace" }}>
                        SLA {item.sla_minutes}m
                      </span>
                      {extractionId && (
                        <span style={{ fontSize: "10px", color: t.dim, fontFamily: "'IBM Plex Mono', monospace" }}>
                          id: {String(extractionId).slice(0, 8)}…
                        </span>
                      )}
                      <span style={{ fontSize: "11px", color: "#3b82f6", marginLeft: "auto", fontWeight: 600 }}>
                        Open →
                      </span>
                    </div>
                  </div>
                );
              })}
            </div>
          </div>

          {/* Escalations — only shown when viewing "all" */}
          {!selectedDoc && escalations.length > 0 && (
            <div style={{ display: "flex", flexDirection: "column", gap: "12px" }}>
              <h2 style={{
                fontSize: "12px", fontWeight: 700, textTransform: "uppercase",
                letterSpacing: ".07em", color: "#f87171", margin: 0,
                display: "flex", alignItems: "center", justifyContent: "space-between",
              }}>
                <span>⚠ SLA Breaches ({escalations.length})</span>
                <button
                  onClick={() => clearEscalationsMutation.mutate()}
                  disabled={clearEscalationsMutation.isPending}
                  style={{
                    background: "rgba(239,68,68,.15)", border: "1px solid rgba(239,68,68,.4)",
                    borderRadius: "6px", color: "#ef4444", fontSize: "10px",
                    fontWeight: 700, padding: "3px 10px", cursor: "pointer",
                    fontFamily: "'Sora', sans-serif",
                    opacity: clearEscalationsMutation.isPending ? 0.5 : 1,
                  }}
                >
                  {clearEscalationsMutation.isPending ? "Clearing…" : "Clear all"}
                </button>
              </h2>
              <div style={{ display: "flex", flexDirection: "column", gap: "8px" }}>
                {escalations.map((esc: any, i: number) => (
                  <div key={esc.item_id ?? i} style={{
                    background: dark ? "rgba(239,68,68,.08)" : "rgba(239,68,68,.04)",
                    border: "1px solid rgba(239,68,68,.6)",
                    borderRadius: "10px", padding: "12px 14px",
                    display: "flex", flexDirection: "column", gap: "6px",
                  }}>
                    <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", gap: "8px" }}>
                      <span style={{ fontSize: "11px", fontWeight: 700, textTransform: "uppercase", letterSpacing: ".05em", color: t.subtext }}>
                        {esc.extraction_result?.field_name ?? "—"}
                      </span>
                      <span style={{ color: "#ef4444", fontWeight: 700, fontSize: "11px", animation: "blink 1.5s infinite" }}>
                        SLA BREACHED
                      </span>
                    </div>
                    <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", gap: "8px" }}>
                      <span style={{ fontSize: "13px", color: t.itemVal }}>
                        {esc.extraction_result?.value ?? "—"}
                      </span>
                      <span style={{ fontSize: "10px", color: t.dim, fontFamily: "'IBM Plex Mono', monospace" }}>
                        breached {new Date((esc.breached_at ?? 0) * 1000).toLocaleTimeString()}
                      </span>
                    </div>
                  </div>
                ))}
              </div>
            </div>
          )}
        </div>
      </div>

      <style>{`
        @import url('https://fonts.googleapis.com/css2?family=IBM+Plex+Mono:wght@400;600&family=Sora:wght@400;600;700&display=swap');
        @keyframes spin  { to { transform: rotate(360deg); } }
        @keyframes blink { 0%,100% { opacity: 1; } 50% { opacity: .4; } }
        @media (max-width: 800px) {
          div[style*="grid-template-columns"] { grid-template-columns: 1fr !important; }
        }
      `}</style>
    </div>
  );
}