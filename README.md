# newtonD

<img src="newton.jpg"  width=252px height=336px >

## What is it?
newtonD is (or rather, soon will be) a structured, leveled, multicast transport logger with parameterization and automatic metric generation capabilities. It contains both a clientside component (the structured, leveled logger) with transport capabilities to a serverside component. The serverside component can (or rather, will soon) aggregate logs as it ingests them, and spit out any aggregated logs to other servers; this allows the computational requirements of traditional observability to be balanced across the compute of the cluster that's being observed. 

newtonD will also eventually support metrics processing and just-in-time collation. In contrast to modern observability platforms, newtonD takes advantage of the fact that servers can function as aggregation relays, and allows each server to do the brunt of the heavy lifting - by declaring specific metrics and monitors in a yaml file (or an API that each server instance hits, I haven't decided yet) to denote namespaced log -> metric type bindings. 


TL;DR: newtonD lets you keep your print statement debugging in your prod codebase, where it'll generate metrics that are aggregated at multiple levels to tell you exactly what's happening in your system with zero effort and minimal observability prep work/configuration. 

## Usage
Use `go run main.go server` to start the server, and then `go run main.go` for the client (which will send random logs over)


In bullets: 

- Leveled & structured Logging, works as you'd expect; JSON and key-value string available with || separator
- All logs, regardless of level, have their parameters converted into metric-form and chucked to the server (TODO: this should only happen for specific transports, such as the socket transport)
- The server works with bubbletea to expose a TUI so I don't have to write frontend code (TODO: make it work with wish so charts and metrics can be viewed directly from the terminal)
- The server may act as a relay for other servers, pushing logs to one or more destinations once it's performed its own aggregations/transformations on them
- Eventually, the server will shard and partition logs to make them indexable and searchable, and I'll find a good way to store time series data that doesn't involve a whole prometheus mess (since I can't replicate that across all servers)
- Because servers are hierarchical, you can always look up servers on a) the host, b) the cluster, c) the region, and d) globally (assuming you've set up your forwarding that way) to get fast results without cruft


Wishlist: 
- UX, could always be better - gotta figure out how wish works with ssh sessions too :( intent is for multiple sessions to share the same server instance with different views of the same data
- Breakpoint logging: mostly a UI problem I think
- Hard monitors (configured and saved) and soft monitors (JIT from ingested metrics, good for exploring bottlenecks): how to do this properly without mangling my data flow?
- Heuristics processing; need to find a way to do that at multiple server levels, without having to invent a DSL
- Configuration management & live tweaking: need to build that "super simple" feature flag library I keep describing when people bring up paying for LaunchDarkly
- 

newtonD was originally built on the afternoon of Jan 25th, 2025 at my monthly household hackathon :) Newton, the lovable labradoodle, generously offered to be the project's mascot in exchange for some bits of popcorn. 