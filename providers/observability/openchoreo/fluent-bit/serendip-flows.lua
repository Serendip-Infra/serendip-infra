-- Serendip's flattening of the network layer's connection records
-- (fluentbit.go, flowScript). One Hubble export line in, one flat record
-- out, or nothing.

local function is_endpoint(id)
  return type(id) == "number" and id >= 256 and id < 16777216
end

local function workload(e)
  local w = e["workloads"]
  if type(w) == "table" and type(w[1]) == "table" then
    return w[1]["name"], w[1]["kind"]
  end
  return nil, nil
end

function serendip_flow(tag, ts, record)
  local f = record["flow"]
  if type(f) ~= "table" then
    return -1, ts, record
  end
  local s = f["source"] or {}
  local d = f["destination"] or {}
  if f["verdict"] == "FORWARDED" and
     ((is_endpoint(s["identity"]) and s["namespace"] == nil) or
      (is_endpoint(d["identity"]) and d["namespace"] == nil)) then
    return -1, ts, record
  end
  local out = {}
  out["_timestamp"] = math.floor(ts * 1000000 + 0.5)
  out["verdict"] = f["verdict"]
  out["drop_reason"] = f["drop_reason_desc"]
  out["direction"] = f["traffic_direction"]
  out["src_namespace"] = s["namespace"]
  out["src_workload"], out["src_kind"] = workload(s)
  out["src_identity"] = s["identity"]
  out["dst_namespace"] = d["namespace"]
  out["dst_workload"], out["dst_kind"] = workload(d)
  out["dst_identity"] = d["identity"]
  local l4 = f["l4"]
  if type(l4) == "table" then
    for proto, v in pairs(l4) do
      out["protocol"] = proto
      if type(v) == "table" then
        out["port"] = v["destination_port"]
      end
    end
  end
  local a = f["aggregate"]
  local n = 0
  if type(a) == "table" then
    n = (a["ingress_flow_count"] or 0) + (a["egress_flow_count"] or 0)
  end
  if n < 1 then
    n = 1
  end
  out["count"] = n
  return 1, ts, out
end
