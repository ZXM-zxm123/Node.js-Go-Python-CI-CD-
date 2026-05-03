#!/usr/bin/env python3
import os
import sys
import subprocess
import glob

def main():
    print("=== Unit Test Plugin ===")
    
    test_files = []
    patterns = ['*test*.py', 'test_*.py', '*_test.py']
    for pattern in patterns:
        test_files.extend(glob.glob(pattern))
    
    if not test_files:
        print("No test files found")
        return 0
    
    print(f"Found {len(test_files)} test file(s): {', '.join(test_files)}")
    
    for test_file in test_files:
        print(f"\nRunning {test_file}...")
        try:
            result = subprocess.run(
                [sys.executable, '-m', 'unittest', test_file],
                capture_output=True,
                text=True
            )
            print(result.stdout)
            if result.stderr:
                print(result.stderr)
            if result.returncode != 0:
                return result.returncode
        except Exception as e:
            print(f"Error running tests: {e}")
            return 1
    
    print("\n✅ All tests passed!")
    return 0

if __name__ == "__main__":
    sys.exit(main())
