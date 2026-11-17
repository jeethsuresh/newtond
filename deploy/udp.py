"""Add the pilot's private UDP ingestion port to the standard Gravitas resources."""
import json
import sys


resources = json.load(sys.stdin)
for resource in resources["items"]:
    kind = resource["kind"]
    if kind == "Deployment":
        container = resource["spec"]["template"]["spec"]["containers"][0]
        container["ports"].append({"name": "events", "protocol": "UDP", "containerPort": 5514})
    elif kind == "Service":
        resource["spec"]["ports"][0]["name"] = "http"
        resource["spec"]["ports"].append(
            {"name": "events", "port": 5514, "protocol": "UDP", "targetPort": "events"}
        )
    elif kind == "NetworkPolicy":
        resource["spec"]["ingress"].append(
            {
                "from": [
                    {
                        "namespaceSelector": {"matchLabels": {"kubernetes.io/metadata.name": name}},
                        "podSelector": {"matchLabels": {"app.kubernetes.io/name": name}},
                    }
                    for name in ("iam", "river")
                ],
                "ports": [{"port": 5514, "protocol": "UDP"}],
            }
        )
json.dump(resources, sys.stdout)
