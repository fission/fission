if ! command -v dashboard-linter >/dev/null 2>&1; then
    echo "dashboard-linter is not installed"
    echo "Installing dashboard-linter..."
    go install github.com/grafana/dashboard-linter@latest
    exit 1;
fi
BASE_PATH=$(pwd)

if [[ -z "$BASE_PATH" ]] ; then
    BASE_PATH=$(GITHUB_WORKSPACE)
fi

DASHBOARD_PATH="$BASE_PATH/charts/fission-all/dashboards/*"

for f in $DASHBOARD_PATH
do
    dashboard-linter lint --strict --verbose $f
done