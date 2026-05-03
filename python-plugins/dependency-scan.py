#!/usr/bin/env python3
import os
import sys
import json
import re

def scan_python_dependencies():
    print("=== Python Dependency Scan ===")
    deps = []
    
    if os.path.exists('requirements.txt'):
        print("Found requirements.txt")
        with open('requirements.txt', 'r') as f:
            for line in f:
                line = line.strip()
                if line and not line.startswith('#'):
                    deps.append(line)
    
    if os.path.exists('pyproject.toml'):
        print("Found pyproject.toml")
        try:
            with open('pyproject.toml', 'r') as f:
                content = f.read()
                matches = re.findall(r'["\']?([a-zA-Z0-9_-]+)["\']?\s*[=~>]', content)
                deps.extend(matches)
        except:
            pass
    
    return deps

def scan_node_dependencies():
    print("=== Node.js Dependency Scan ===")
    deps = []
    
    if os.path.exists('package.json'):
        print("Found package.json")
        try:
            with open('package.json', 'r') as f:
                data = json.load(f)
                if 'dependencies' in data:
                    deps.extend(data['dependencies'].keys())
                if 'devDependencies' in data:
                    deps.extend(data['devDependencies'].keys())
        except:
            pass
    
    return deps

def main():
    print("=== Dependency Scan Plugin ===")
    
    python_deps = scan_python_dependencies()
    node_deps = scan_node_dependencies()
    
    if python_deps:
        print(f"\n📦 Python dependencies ({len(python_deps)}):")
        for dep in python_deps[:10]:
            print(f"  - {dep}")
        if len(python_deps) > 10:
            print(f"  ... and {len(python_deps) - 10} more")
    
    if node_deps:
        print(f"\n📦 Node.js dependencies ({len(node_deps)}):")
        for dep in node_deps[:10]:
            print(f"  - {dep}")
        if len(node_deps) > 10:
            print(f"  ... and {len(node_deps) - 10} more")
    
    if not python_deps and not node_deps:
        print("\nNo dependencies found")
    
    print("\n✅ Dependency scan complete!")
    return 0

if __name__ == "__main__":
    sys.exit(main())
